//go:build latencyprobe && !windows

package main

// Instrumentation for the opt-in private benchmark binary only. Normal builds
// have no profiling entry point. The harness signals its own PID after sampling
// ends, then waits for the marker before gracefully quitting Marshal.
import (
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"syscall"
)

func init() {
	prefix := os.Getenv("MARSHAL_LATENCY_PROFILE_PREFIX")
	if prefix == "" {
		return
	}
	cpu, err := os.OpenFile(prefix+".cpu.pprof", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	quiet := os.Getenv("MARSHAL_LATENCY_PROFILE_MODE") == "quiet"
	if !quiet {
		if err := pprof.StartCPUProfile(cpu); err != nil {
			panic(err)
		}
	}
	requests := make(chan os.Signal, 2)
	signal.Notify(requests, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		if quiet {
			if sig := <-requests; sig != syscall.SIGUSR1 {
				panic("quiet profile requires start signal")
			}
			initial, err := os.OpenFile(prefix+".initial.heap.pprof", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				panic(err)
			}
			runtime.GC()
			if err := pprof.WriteHeapProfile(initial); err != nil {
				panic(err)
			}
			_ = initial.Close()
			if err := pprof.StartCPUProfile(cpu); err != nil {
				panic(err)
			}
			if err := os.WriteFile(prefix+".started", []byte("started\n"), 0600); err != nil {
				panic(err)
			}
		}
		if sig := <-requests; sig != syscall.SIGUSR2 {
			panic("profile requires stop signal")
		}
		pprof.StopCPUProfile()
		_ = cpu.Close()
		heap, err := os.OpenFile(prefix+".heap.pprof", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		runtime.GC()
		if err := pprof.WriteHeapProfile(heap); err != nil {
			panic(err)
		}
		if err := heap.Close(); err != nil {
			panic(err)
		}
		if err := os.WriteFile(prefix+".done", []byte("complete\n"), 0600); err != nil {
			panic(err)
		}
		signal.Stop(requests)
	}()
}
