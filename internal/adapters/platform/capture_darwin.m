#import <Cocoa/Cocoa.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>

// The copied completion block retains this box even when the caller times out.
@interface RDResult : NSObject {
@public
    dispatch_semaphore_t done;
    SCShareableContent* content;
    CGImageRef image;
    NSString* failure;
}
@end
@implementation RDResult
- (instancetype)init { self=[super init]; if(self) done=dispatch_semaphore_create(0); return self; }
- (void)dealloc { if(content)[content release];if(image)CGImageRelease(image);[failure release];dispatch_release(done);[super dealloc]; }
@end

// Called only from a Go worker, never from the Cocoa main thread.
static SCShareableContent* rd_content(char** error) {
    RDResult* result=[[RDResult alloc] init];
    [SCShareableContent getShareableContentExcludingDesktopWindows:YES onScreenWindowsOnly:YES completionHandler:^(SCShareableContent* c, NSError* e) {
        if (!e) result->content = [c retain];
        else result->failure = [e.localizedDescription copy];
        dispatch_semaphore_signal(result->done);
    }];
    if(dispatch_semaphore_wait(result->done, dispatch_time(DISPATCH_TIME_NOW,5*NSEC_PER_SEC))!=0) {
        *error=strdup("ScreenCaptureKit: shareable-content timeout");[result release];return nil;
    }
    SCShareableContent* content=[result->content retain];
    if(!content) *error=strdup(result->failure.UTF8String ?: "ScreenCaptureKit: no shareable content");
    [result release];
    return content;
}

char* rd_sources(char** error) {
    @autoreleasepool {
        SCShareableContent* content = rd_content(error);
        if (!content) return NULL;
        NSMutableArray* items = [NSMutableArray array];
        for (SCWindow* w in content.windows) {
            if (w.frame.size.width < 1 || w.frame.size.height < 1) continue;
            NSString* title = [NSString stringWithFormat:@"%@ — %@", w.owningApplication.applicationName ?: @"", w.title ?: @""];
            [items addObject:@{@"id": @(w.windowID), @"title": title}];
        }
        NSData* data = [NSJSONSerialization dataWithJSONObject:items options:0 error:nil];
        char* result = calloc(data.length+1,1);
        if (result) memcpy(result,data.bytes,data.length);
        [content release];
        return result;
    }
}

unsigned char* rd_capture(unsigned int id, int* width, int* height, char** error) {
    @autoreleasepool {
        SCShareableContent* content = rd_content(error);
        if (!content) return NULL;
        SCWindow* window=nil;
        for (SCWindow* w in content.windows) if (w.windowID==id) { window=w; break; }
        if (!window) { [content release]; *error=strdup("ScreenCaptureKit: selected window unavailable"); return NULL; }
        SCContentFilter* filter=[[SCContentFilter alloc] initWithDesktopIndependentWindow:window];
        SCStreamConfiguration* config=[[SCStreamConfiguration alloc] init];
        config.width=(size_t)(filter.contentRect.size.width*filter.pointPixelScale);
        config.height=(size_t)(filter.contentRect.size.height*filter.pointPixelScale);
        config.showsCursor=NO;
        if (config.width<1 || config.height<1 || config.width>16384 || config.height>16384 || config.width*config.height>64*1024*1024) {
            [filter release]; [config release]; [content release]; *error=strdup("ScreenCaptureKit: invalid capture dimensions"); return NULL;
        }
        RDResult* result=[[RDResult alloc] init];
        [SCScreenshotManager captureImageWithFilter:filter configuration:config completionHandler:^(CGImageRef captured,NSError* e) {
            if (captured && !e) result->image=CGImageRetain(captured);
            else result->failure=[(e.localizedDescription ?: @"ScreenCaptureKit: empty image") copy];
            dispatch_semaphore_signal(result->done);
        }];
        long timedOut=dispatch_semaphore_wait(result->done,dispatch_time(DISPATCH_TIME_NOW,5*NSEC_PER_SEC));
        [filter release]; [config release]; [content release];
        if(timedOut) { *error=strdup("ScreenCaptureKit: screenshot timeout");[result release];return NULL; }
        CGImageRef image=result->image ? CGImageRetain(result->image) : NULL;
        if (!image) { *error=strdup(result->failure.UTF8String ?: "ScreenCaptureKit: empty image");[result release];return NULL; }
        [result release];
        size_t w=CGImageGetWidth(image),h=CGImageGetHeight(image);
        if (w>16384 || h>16384 || w*h>64*1024*1024) { CGImageRelease(image); *error=strdup("ScreenCaptureKit: image too large"); return NULL; }
        unsigned char* pixels=calloc(w*h,4);
        CGColorSpaceRef colors=CGColorSpaceCreateDeviceRGB();
        CGContextRef ctx=pixels ? CGBitmapContextCreate(pixels,w,h,8,w*4,colors,kCGImageAlphaPremultipliedLast|kCGBitmapByteOrder32Big) : NULL;
        CGColorSpaceRelease(colors);
        if (!ctx) { free(pixels); CGImageRelease(image); *error=strdup("ScreenCaptureKit: allocation failed"); return NULL; }
        CGContextDrawImage(ctx,CGRectMake(0,0,w,h),image);
        CGContextRelease(ctx); CGImageRelease(image); *width=(int)w;*height=(int)h;
        return pixels;
    }
}
