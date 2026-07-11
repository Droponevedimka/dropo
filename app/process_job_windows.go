//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var managedProcessJob = struct {
	sync.Mutex
	handle windows.Handle
}{
	handle: 0,
}

func ensureManagedProcessJob(logger func(string)) error {
	managedProcessJob.Lock()
	defer managedProcessJob.Unlock()

	if managedProcessJob.handle != 0 {
		return nil
	}

	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create job object: %w", err)
	}

	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		handle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(handle)
		return fmt.Errorf("enable kill-on-close job limit: %w", err)
	}

	managedProcessJob.handle = handle
	if logger != nil {
		logger("[ProcessJob] Windows job object ready (kill-on-close)")
	}
	return nil
}

func attachManagedCmdToJob(cmd *exec.Cmd, label string, logger func(string)) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := ensureManagedProcessJob(logger); err != nil {
		if logger != nil {
			logger(fmt.Sprintf("[ProcessJob] failed to create job for %s pid=%d: %v", label, cmd.Process.Pid, err))
		}
		return
	}

	managedProcessJob.Lock()
	job := managedProcessJob.handle
	managedProcessJob.Unlock()
	if job == 0 {
		return
	}

	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		if logger != nil {
			logger(fmt.Sprintf("[ProcessJob] failed to open %s pid=%d: %v", label, cmd.Process.Pid, err))
		}
		return
	}
	defer windows.CloseHandle(process)

	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		if logger != nil {
			logger(fmt.Sprintf("[ProcessJob] failed to attach %s pid=%d: %v", label, cmd.Process.Pid, err))
		}
		return
	}
	if logger != nil {
		logger(fmt.Sprintf("[ProcessJob] attached %s pid=%d", label, cmd.Process.Pid))
	}
}

func closeManagedProcessJob(logger func(string)) {
	managedProcessJob.Lock()
	handle := managedProcessJob.handle
	managedProcessJob.handle = 0
	managedProcessJob.Unlock()

	if handle == 0 {
		return
	}
	if err := windows.CloseHandle(handle); err != nil {
		if logger != nil {
			logger(fmt.Sprintf("[ProcessJob] failed to close job object: %v", err))
		}
		return
	}
	if logger != nil {
		logger("[ProcessJob] closed job object")
	}
}
