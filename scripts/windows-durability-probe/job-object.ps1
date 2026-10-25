# Controller-only interop. Never load this into the temporary user's process.
if (-not ('RasterDockProbeJob' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;

public sealed class RasterDockProbeJob : SafeHandleZeroOrMinusOneIsInvalid
{
    public RasterDockProbeJob() : base(true) { }
    protected override bool ReleaseHandle() { return CloseHandle(handle); }

    [StructLayout(LayoutKind.Sequential)]
    private struct BasicLimitInformation
    {
        public long PerProcessUserTimeLimit, PerJobUserTimeLimit;
        public uint LimitFlags;
        public UIntPtr MinimumWorkingSetSize, MaximumWorkingSetSize;
        public uint ActiveProcessLimit;
        public UIntPtr Affinity;
        public uint PriorityClass, SchedulingClass;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct IoCounters
    {
        public ulong ReadOperationCount, WriteOperationCount, OtherOperationCount;
        public ulong ReadTransferCount, WriteTransferCount, OtherTransferCount;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct ExtendedLimitInformation
    {
        public BasicLimitInformation BasicLimitInformation;
        public IoCounters IoInfo;
        public UIntPtr ProcessMemoryLimit, JobMemoryLimit, PeakProcessMemoryUsed, PeakJobMemoryUsed;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct BasicAccountingInformation
    {
        public long TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime;
        public uint TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses;
    }

    private const uint JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x2000;
    private const int JobObjectBasicAccountingInformation = 1;
    private const int JobObjectExtendedLimitInformation = 9;
    private const uint PROCESS_QUERY_LIMITED_INFORMATION = 0x1000;

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern RasterDockProbeJob CreateJobObject(IntPtr securityAttributes, string name);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool SetInformationJobObject(RasterDockProbeJob job, int infoClass, ref ExtendedLimitInformation info, uint length);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool QueryInformationJobObject(RasterDockProbeJob job, int infoClass, out BasicAccountingInformation info, uint length, out uint returned);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool AssignProcessToJobObject(RasterDockProbeJob job, IntPtr process);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool IsProcessInJob(IntPtr process, RasterDockProbeJob job, [MarshalAs(UnmanagedType.Bool)] out bool inJob);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool TerminateJobObject(RasterDockProbeJob job, uint exitCode);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern SafeFileHandle OpenProcess(uint desiredAccess, bool inheritHandle, uint processId);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool CloseHandle(IntPtr handle);

    private static void Check(bool success, string operation)
    {
        if (!success) throw new Win32Exception(Marshal.GetLastWin32Error(), operation);
    }

    public static RasterDockProbeJob Create()
    {
        RasterDockProbeJob job = CreateJobObject(IntPtr.Zero, null); // unnamed, controller-owned, non-inheritable
        if (job == null || job.IsInvalid)
        {
            int error = Marshal.GetLastWin32Error();
            if (job != null) job.Dispose();
            throw new Win32Exception(error, "CreateJobObject");
        }
        try
        {
            ExtendedLimitInformation limits = new ExtendedLimitInformation();
            limits.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
            Check(SetInformationJobObject(job, JobObjectExtendedLimitInformation, ref limits,
                (uint)Marshal.SizeOf(typeof(ExtendedLimitInformation))), "SetInformationJobObject");
            return job;
        }
        catch { job.Dispose(); throw; }
    }

    public void Assign(IntPtr process)
    {
        Check(AssignProcessToJobObject(this, process), "AssignProcessToJobObject");
        if (!Contains(process)) throw new InvalidOperationException("Assigned wrapper is not in owned job.");
    }

    public bool Contains(IntPtr process)
    {
        bool inJob;
        Check(IsProcessInJob(process, this, out inJob), "IsProcessInJob");
        return inJob;
    }

    public bool ContainsPid(uint pid)
    {
        using (SafeFileHandle process = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, pid))
        {
            if (process.IsInvalid) throw new Win32Exception(Marshal.GetLastWin32Error(), "OpenProcess fixture");
            return Contains(process.DangerousGetHandle());
        }
    }

    public uint ActiveProcesses()
    {
        BasicAccountingInformation info;
        uint returned;
        Check(QueryInformationJobObject(this, JobObjectBasicAccountingInformation, out info,
            (uint)Marshal.SizeOf(typeof(BasicAccountingInformation)), out returned), "QueryInformationJobObject");
        if (returned < (uint)Marshal.SizeOf(typeof(BasicAccountingInformation)))
            throw new InvalidOperationException("Truncated job accounting response.");
        return info.ActiveProcesses;
    }

    public void Terminate() { Check(TerminateJobObject(this, 1), "TerminateJobObject"); }
}
'@ -ErrorAction Stop
}
