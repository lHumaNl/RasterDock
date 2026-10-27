#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#import <mach/mach_time.h>
#import <pthread.h>
#import "control_capture_darwin.h"

static const size_t RDMaxDimension = 16384, RDMaxPixels = 64 * 1024 * 1024;
static const NSInteger RDNativeQueueDepth = 3;
static pthread_mutex_t RDOwnerLock = PTHREAD_MUTEX_INITIALIZER;
static id RDOwner;
typedef NS_ENUM(int, RDPhase) { RDContent, RDStarting, RDRunning, RDStopping, RDFinished };

@interface RDControlCapture : NSObject <SCStreamOutput, SCStreamDelegate> {
@public
    dispatch_queue_t queue;
    SCStream *stream;
    RDControlFrame slot;
    RDPhase phase;
    int state;
    BOOL revoked, done, hasGeometry, outputInstalled;
    CGSize sourceSize;
    CGRect geometry;
    double scale, contentScale;
    size_t width, height;
    RDControlTarget target;
    BOOL identityPending;
}
- (void)begin:(uint32_t)window;
- (void)configure:(SCWindow *)window;
- (void)shutdown;
- (void)finish;
- (void)invalidate:(int)reason;
- (BOOL)validateSource;
@end

static BOOL rd_dimensions(size_t w, size_t h) {
    return w && h && w <= RDMaxDimension && h <= RDMaxDimension && w <= RDMaxPixels / h;
}

static SCStreamConfiguration *rd_configuration(SCContentFilter *filter) {
    double w = filter.contentRect.size.width * filter.pointPixelScale;
    double h = filter.contentRect.size.height * filter.pointPixelScale;
    if (!isfinite(w) || !isfinite(h) || w < 1 || h < 1 || w > RDMaxDimension || h > RDMaxDimension) return nil;
    if (!rd_dimensions((size_t)w,(size_t)h)) return nil;
    SCStreamConfiguration *config = [[SCStreamConfiguration alloc] init];
    config.width = (size_t)w; config.height = (size_t)h; config.showsCursor = NO;
    config.pixelFormat = kCVPixelFormatType_32BGRA; config.queueDepth = RDNativeQueueDepth;
    config.ignoreShadowsSingleWindow = YES;
    return config;
}

static NSDictionary *rd_attachments(CMSampleBufferRef sample) {
    if (!CMSampleBufferIsValid(sample) || !CMSampleBufferDataIsReady(sample)) return nil;
    CFArrayRef values = CMSampleBufferGetSampleAttachmentsArray(sample, false);
    if (!values || CFArrayGetCount(values) != 1) return nil;
    id value = (id)CFArrayGetValueAtIndex(values, 0);
    return [value isKindOfClass:[NSDictionary class]] ? value : nil;
}

static BOOL rd_geometry(NSDictionary *info, CGRect *rect, double *scale, double *contentScale) {
    id r = info[SCStreamFrameInfoContentRect];
    id s = info[SCStreamFrameInfoScaleFactor], c = info[SCStreamFrameInfoContentScale];
    if (![r isKindOfClass:[NSDictionary class]] || ![s isKindOfClass:[NSNumber class]] ||
        ![c isKindOfClass:[NSNumber class]]) return NO;
    if (!CGRectMakeWithDictionaryRepresentation((CFDictionaryRef)r, rect)) return NO;
    *scale = [s doubleValue]; *contentScale = [c doubleValue];
    return isfinite(rect->origin.x) && isfinite(rect->origin.y) &&
        isfinite(rect->size.width) && isfinite(rect->size.height) &&
        rect->size.width > 0 && rect->size.height > 0 &&
        isfinite(*scale) && *scale >= 1 && *scale <= 4 &&
        isfinite(*contentScale) && *contentScale > 0;
}

static unsigned char *rd_copy_pixels(CVPixelBufferRef pixel, size_t w, size_t h) {
    if (CVPixelBufferLockBaseAddress(pixel, kCVPixelBufferLock_ReadOnly) != kCVReturnSuccess) return NULL;
    size_t pitch = CVPixelBufferGetBytesPerRow(pixel), size = CVPixelBufferGetDataSize(pixel);
    unsigned char *src = CVPixelBufferGetBaseAddress(pixel), *out = NULL;
    if (src && pitch >= w * 4 && size >= w * 4 && h - 1 <= (size - w * 4) / pitch)
        out = malloc(w * h * 4);
    if (out) for (size_t y = 0; y < h; y++) for (size_t x = 0; x < w; x++) {
        size_t s = y * pitch + x * 4, d = (y * w + x) * 4;
        out[d] = src[s+2]; out[d+1] = src[s+1]; out[d+2] = src[s]; out[d+3] = src[s+3];
    }
    CVPixelBufferUnlockBaseAddress(pixel, kCVPixelBufferLock_ReadOnly);
    return out;
}

@implementation RDControlCapture
- (instancetype)init {
    self = [super init];
    if (self) queue = dispatch_queue_create("rasterdock.control.capture",
        dispatch_queue_attr_make_with_autorelease_frequency(DISPATCH_QUEUE_SERIAL, DISPATCH_AUTORELEASE_FREQUENCY_WORK_ITEM));
    return self;
}
- (void)dealloc {
    free(target.title); free(slot.pixels); [stream release]; dispatch_release(queue); [super dealloc];
}
- (void)invalidate:(int)reason {
    @synchronized(self) {
        if (revoked) return;
        state = reason;
        revoked = YES; free(slot.pixels); slot = (RDControlFrame){0};
    }
    dispatch_async(queue, ^{ [self shutdown]; });
}
- (void)finish {
    if (outputInstalled) {
        NSError *error = nil;
        if (![stream removeStreamOutput:self type:SCStreamOutputTypeScreen error:&error]) return;
        outputInstalled = NO;
    }
    // Remove the output first, then drain already-enqueued sample callbacks.
    dispatch_async(queue, ^{ [self reap]; });
}
- (void)reap {
    [stream release]; stream = nil; phase = RDFinished;
    @synchronized(self) { done = YES; }
    pthread_mutex_lock(&RDOwnerLock);
    if (RDOwner == self) { [RDOwner release]; RDOwner = nil; }
    pthread_mutex_unlock(&RDOwnerLock);
}
- (void)shutdown {
    if (phase != RDRunning) return; // Opening callbacks own the eventual shutdown.
    phase = RDStopping;
    [stream stopCaptureWithCompletionHandler:^(NSError *error) {
        // An unsuccessful stop cannot prove quiescence: keep the busy latch owned.
        if (!error) dispatch_async(queue, ^{ [self finish]; });
    }];
}
- (void)begin:(uint32_t)window {
    [SCShareableContent getShareableContentExcludingDesktopWindows:YES onScreenWindowsOnly:YES
        completionHandler:^(SCShareableContent *content, NSError *error) {
        dispatch_async(queue, ^{
            @synchronized(self) { if (revoked) { [self finish]; return; } }
            SCWindow *selected = nil;
            for (SCWindow *candidate in content.windows) if (candidate.windowID == window) selected = candidate;
            if (error || !selected) { [self invalidate:RDControlInvalid]; [self finish]; return; }
            [self configure:selected];
        });
    }];
}
- (void)configure:(SCWindow *)window {
    if (![self pinSource:window]) { [self invalidate:RDControlInvalid]; [self finish]; return; }
    sourceSize = window.frame.size;
    SCContentFilter *filter = [[SCContentFilter alloc] initWithDesktopIndependentWindow:window];
    SCStreamConfiguration *config = rd_configuration(filter);
    if (!config) { [filter release]; [self invalidate:RDControlInvalid]; [self finish]; return; }
    width = config.width; height = config.height;
    stream = [[SCStream alloc] initWithFilter:filter configuration:config delegate:self];
    [filter release]; [config release];
    NSError *error = nil;
    if (![stream addStreamOutput:self type:SCStreamOutputTypeScreen sampleHandlerQueue:queue error:&error]) {
        [self invalidate:RDControlInvalid]; [self finish]; return;
    }
    outputInstalled = YES;
    [self start];
}
- (void)start {
    if (![self validateSource]) { [self finish]; return; }
    phase = RDStarting;
    [stream startCaptureWithCompletionHandler:^(NSError *error) {
        dispatch_async(queue, ^{
            if (error) { [self invalidate:RDControlInvalid]; [self finish]; return; }
            phase = RDRunning;
            [self validateSource];
            @synchronized(self) { if (!revoked) state = RDControlReady; }
            @synchronized(self) { if (revoked) [self shutdown]; }
        });
    }];
}
- (BOOL)pinSource:(SCWindow *)window {
    pid_t pid = window.owningApplication.processID;
    uint64_t started = rd_control_process_start(pid);
    const char *title = window.title.UTF8String;
    if (!window.windowID || !started || !title || !title[0]) return NO;
    char *ownedTitle = strdup(title);
    if (!ownedTitle) return NO;
    @synchronized(self) { target = (RDControlTarget){window.windowID,pid,started,ownedTitle}; }
    return [self validateSource];
}
- (BOOL)validateSource {
    RDControlTarget pinned;
    @synchronized(self) { if (revoked) return NO; pinned = target; }
    if (rd_control_identity_matches(pinned)) return YES;
    [self invalidate:RDControlInvalid]; return NO;
}
- (BOOL)validateGeometry:(NSDictionary *)info {
    CGRect rect; double s, c;
    if (!rd_geometry(info, &rect, &s, &c)) { [self invalidate:RDControlInvalid]; return NO; }
    CGRect screenRect;
    id screen = info[SCStreamFrameInfoScreenRect];
    if (![screen isKindOfClass:[NSDictionary class]] || !CGRectMakeWithDictionaryRepresentation((CFDictionaryRef)screen,&screenRect)) {
        [self invalidate:RDControlInvalid]; return NO;
    }
    if (!CGSizeEqualToSize(screenRect.size,sourceSize)) { [self invalidate:RDControlResized]; return NO; }
    if (hasGeometry && (!CGRectEqualToRect(rect,geometry) || s != scale || c != contentScale)) {
        [self invalidate:RDControlResized]; return NO;
    }
    geometry = rect; scale = s; contentScale = c; hasGeometry = YES;
    return YES;
}
- (void)accept:(CMSampleBufferRef)sample info:(NSDictionary *)info {
    id display = info[SCStreamFrameInfoDisplayTime];
    if (![display isKindOfClass:[NSNumber class]] || [display longLongValue] <= 0) { [self invalidate:RDControlInvalid]; return; }
    if (CFGetTypeID((CFTypeRef)display) != CFNumberGetTypeID() || CFNumberIsFloatType((CFNumberRef)display)) { [self invalidate:RDControlInvalid]; return; }
    if (![self validateGeometry:info]) return;
    CVPixelBufferRef pixel = CMSampleBufferGetImageBuffer(sample);
    if (!pixel || CVPixelBufferGetPixelFormatType(pixel) != kCVPixelFormatType_32BGRA || CVPixelBufferIsPlanar(pixel)) { [self invalidate:RDControlInvalid]; return; }
    size_t w = CVPixelBufferGetWidth(pixel), h = CVPixelBufferGetHeight(pixel);
    if (w != width || h != height) { [self invalidate:RDControlResized]; return; }
    unsigned char *pixels = rd_copy_pixels(pixel,w,h);
    if (!pixels) { [self invalidate:RDControlInvalid]; return; }
    @synchronized(self) {
        if (revoked) { free(pixels); return; }
        free(slot.pixels); slot = (RDControlFrame){pixels,w,h,[display unsignedLongLongValue]};
    }
}
- (void)stream:(SCStream *)sender didOutputSampleBuffer:(CMSampleBufferRef)sample ofType:(SCStreamOutputType)type {
    @autoreleasepool {
        @synchronized(self) { if (revoked) return; }
        if (![self validateSource]) return;
        if (type != SCStreamOutputTypeScreen) return;
        NSDictionary *info = rd_attachments(sample);
        id status = info[SCStreamFrameInfoStatus];
        if (![status isKindOfClass:[NSNumber class]]) { [self invalidate:RDControlInvalid]; return; }
        if ([status integerValue] == SCFrameStatusIdle) return; // Never retimestamp a repeated surface.
        if ([status integerValue] == SCFrameStatusStarted && !hasGeometry) return;
        if ([status integerValue] != SCFrameStatusComplete) { [self invalidate:RDControlInvalid]; return; }
        [self accept:sample info:info];
    }
}
- (void)stream:(SCStream *)sender didStopWithError:(NSError *)error {
    [self invalidate:RDControlInvalid];
}
@end

int rd_control_capability(void) {
    pthread_mutex_lock(&RDOwnerLock);
    BOOL busy = RDOwner != nil;
    pthread_mutex_unlock(&RDOwnerLock);
    if (busy) return 3;
    if (@available(macOS 14.0, *)) return CGPreflightScreenCaptureAccess() ? 0 : 2;
    return 1;
}
void *rd_control_open(uint32_t window) {
    @autoreleasepool {
        pthread_mutex_lock(&RDOwnerLock);
        if (RDOwner) { pthread_mutex_unlock(&RDOwnerLock); return NULL; }
        RDControlCapture *owner = [[RDControlCapture alloc] init];
        RDOwner = [owner retain];
        pthread_mutex_unlock(&RDOwnerLock);
        dispatch_async(owner->queue, ^{ [owner begin:window]; });
        return owner;
    }
}
int rd_control_state(void *handle) {
    RDControlCapture *owner = handle;
    RDControlTarget pinned;
    @synchronized(owner) { pinned = owner->target; }
    if (pinned.started && rd_control_process_start(pinned.pid) != pinned.started)
        [owner invalidate:RDControlInvalid];
    @synchronized(owner) { return owner->state; }
}
int rd_control_target(void *handle, RDControlTarget *target) {
    RDControlCapture *owner = handle;
    @synchronized(owner) {
        if (owner->revoked || owner->state != RDControlReady) return 0;
        *target = owner->target;
        target->title = strdup(owner->target.title);
        return target->title != NULL;
    }
}
void rd_control_validate(void *handle) {
    RDControlCapture *owner = handle;
    @synchronized(owner) {
        if (owner->revoked || owner->identityPending) return;
        owner->identityPending = YES;
    }
    dispatch_async(owner->queue, ^{
        [owner validateSource];
        @synchronized(owner) { owner->identityPending = NO; }
    });
}
int rd_control_validation_state(void *handle) {
    RDControlCapture *owner = handle;
    @synchronized(owner) {
        if (owner->revoked) return owner->state;
        return owner->identityPending ? RDControlPending : owner->state;
    }
}
int rd_control_take(void *handle, RDControlFrame *frame) {
    RDControlCapture *owner = handle;
    @synchronized(owner) {
        if (owner->revoked) return owner->state;
        if (!owner->slot.pixels) return RDControlPending;
        *frame = owner->slot; owner->slot = (RDControlFrame){0}; return RDControlReady;
    }
}
void rd_control_revoke(void *handle) { [(RDControlCapture *)handle invalidate:RDControlClosed]; }
int rd_control_done(void *handle) {
    RDControlCapture *owner = handle;
    @synchronized(owner) { return owner->done; }
}
void rd_control_release(void *handle) { [(RDControlCapture *)handle release]; }
RDControlTicks rd_control_ticks(void) {
    mach_timebase_info_data_t base;
    if (mach_timebase_info(&base) != KERN_SUCCESS) return (RDControlTicks){0};
    uint64_t a0 = mach_absolute_time(), continuous = mach_continuous_time(), a1 = mach_absolute_time();
    return (RDControlTicks){a0,continuous,a1,base.numer,base.denom};
}
