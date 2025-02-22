package link

import (
	"errors"
	"fmt"
	"go/build"
	"os"
	"os/exec"
	"path"
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/internal/testutils"
	"github.com/cilium/ebpf/internal/unix"
)

var (
	bashEx, _ = OpenExecutable("/bin/bash")
	bashSym   = "main"
)

func TestExecutable(t *testing.T) {
	_, err := OpenExecutable("")
	if err == nil {
		t.Fatal("create executable: expected error on empty path")
	}

	if bashEx.path != "/bin/bash" {
		t.Fatalf("create executable: unexpected path '%s'", bashEx.path)
	}

	_, err = bashEx.offset(bashSym, &UprobeOptions{})
	if err != nil {
		t.Fatalf("find offset: %v", err)
	}

	_, err = bashEx.offset("bogus", &UprobeOptions{})
	if err == nil {
		t.Fatal("find symbol: expected error")
	}
}

func TestExecutableOffset(t *testing.T) {
	c := qt.New(t)

	symbolOffset, err := bashEx.offset(bashSym, &UprobeOptions{})
	if err != nil {
		t.Fatal(err)
	}

	offset, err := bashEx.offset(bashSym, &UprobeOptions{Offset: 0x1})
	if err != nil {
		t.Fatal(err)
	}
	c.Assert(offset, qt.Equals, uint64(0x1))

	offset, err = bashEx.offset(bashSym, &UprobeOptions{RelativeOffset: 0x2})
	if err != nil {
		t.Fatal(err)
	}
	c.Assert(offset, qt.Equals, symbolOffset+0x2)

	offset, err = bashEx.offset(bashSym, &UprobeOptions{Offset: 0x1, RelativeOffset: 0x2})
	if err != nil {
		t.Fatal(err)
	}
	c.Assert(offset, qt.Equals, uint64(0x1+0x2))
}

func TestUprobe(t *testing.T) {
	c := qt.New(t)

	prog := mustLoadProgram(t, ebpf.Kprobe, 0, "")

	up, err := bashEx.Uprobe(bashSym, prog, nil)
	c.Assert(err, qt.IsNil)
	defer up.Close()

	testLink(t, up, prog)
}

func TestUprobeExtNotFound(t *testing.T) {
	prog := mustLoadProgram(t, ebpf.Kprobe, 0, "")

	// This symbol will not be present in Executable (elf.SHN_UNDEF).
	_, err := bashEx.Uprobe("open", prog, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUprobeExtWithOpts(t *testing.T) {
	prog := mustLoadProgram(t, ebpf.Kprobe, 0, "")

	// This Uprobe is broken and will not work because the offset is not
	// correct. This is expected since the offset is provided by the user.
	up, err := bashEx.Uprobe("open", prog, &UprobeOptions{Offset: 0x1})
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
}

func TestUprobeWithPID(t *testing.T) {
	prog := mustLoadProgram(t, ebpf.Kprobe, 0, "")

	up, err := bashEx.Uprobe(bashSym, prog, &UprobeOptions{PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
}

func TestUprobeWithNonExistentPID(t *testing.T) {
	prog := mustLoadProgram(t, ebpf.Kprobe, 0, "")

	// trying to open a perf event on a non-existent PID will return ESRCH.
	_, err := bashEx.Uprobe(bashSym, prog, &UprobeOptions{PID: -2})
	if !errors.Is(err, unix.ESRCH) {
		t.Fatalf("expected ESRCH, got %v", err)
	}
}

func TestUretprobe(t *testing.T) {
	c := qt.New(t)

	prog := mustLoadProgram(t, ebpf.Kprobe, 0, "")

	up, err := bashEx.Uretprobe(bashSym, prog, nil)
	c.Assert(err, qt.IsNil)
	defer up.Close()

	testLink(t, up, prog)
}

// Test u(ret)probe creation using perf_uprobe PMU.
func TestUprobeCreatePMU(t *testing.T) {
	// Requires at least 4.17 (e12f03d7031a "perf/core: Implement the 'perf_kprobe' PMU")
	testutils.SkipOnOldKernel(t, "4.17", "perf_kprobe PMU")

	c := qt.New(t)

	// Fetch the offset from the /bin/bash Executable already defined.
	off, err := bashEx.offset(bashSym, &UprobeOptions{})
	c.Assert(err, qt.IsNil)

	// Prepare probe args.
	args := probeArgs{
		symbol: bashSym,
		path:   bashEx.path,
		offset: off,
		pid:    perfAllThreads,
	}

	// uprobe PMU
	pu, err := pmuUprobe(args)
	c.Assert(err, qt.IsNil)
	defer pu.Close()

	c.Assert(pu.typ, qt.Equals, uprobeEvent)

	// uretprobe PMU
	args.ret = true
	pr, err := pmuUprobe(args)
	c.Assert(err, qt.IsNil)
	defer pr.Close()

	c.Assert(pr.typ, qt.Equals, uretprobeEvent)
}

// Test fallback behaviour on kernels without perf_uprobe PMU available.
func TestUprobePMUUnavailable(t *testing.T) {
	c := qt.New(t)

	// Fetch the offset from the /bin/bash Executable already defined.
	off, err := bashEx.offset(bashSym, &UprobeOptions{})
	c.Assert(err, qt.IsNil)

	// Prepare probe args.
	args := probeArgs{
		symbol: bashSym,
		path:   bashEx.path,
		offset: off,
		pid:    perfAllThreads,
	}

	pk, err := pmuUprobe(args)
	if err == nil {
		pk.Close()
		t.Skipf("Kernel supports perf_uprobe PMU, not asserting error.")
	}

	// Expect ErrNotSupported.
	c.Assert(errors.Is(err, ErrNotSupported), qt.IsTrue, qt.Commentf("got error: %s", err))
}

// Test tracefs u(ret)probe creation on all kernel versions.
func TestUprobeTraceFS(t *testing.T) {
	c := qt.New(t)

	// Fetch the offset from the /bin/bash Executable already defined.
	off, err := bashEx.offset(bashSym, &UprobeOptions{})
	c.Assert(err, qt.IsNil)

	// Prepare probe args.
	args := probeArgs{
		symbol: sanitizeSymbol(bashSym),
		path:   bashEx.path,
		offset: off,
		pid:    perfAllThreads,
	}

	// Open and close tracefs u(ret)probes, checking all errors.
	up, err := tracefsUprobe(args)
	c.Assert(err, qt.IsNil)
	c.Assert(up.Close(), qt.IsNil)
	c.Assert(up.typ, qt.Equals, uprobeEvent)

	args.ret = true
	up, err = tracefsUprobe(args)
	c.Assert(err, qt.IsNil)
	c.Assert(up.Close(), qt.IsNil)
	c.Assert(up.typ, qt.Equals, uretprobeEvent)

	// Create two identical trace events, ensure their IDs differ.
	args.ret = false
	u1, err := tracefsUprobe(args)
	c.Assert(err, qt.IsNil)
	defer u1.Close()
	c.Assert(u1.tracefsID, qt.Not(qt.Equals), 0)

	u2, err := tracefsUprobe(args)
	c.Assert(err, qt.IsNil)
	defer u2.Close()
	c.Assert(u2.tracefsID, qt.Not(qt.Equals), 0)

	// Compare the uprobes' tracefs IDs.
	c.Assert(u1.tracefsID, qt.Not(qt.CmpEquals()), u2.tracefsID)
}

// Test u(ret)probe creation writing directly to <tracefs>/uprobe_events.
// Only runs on 5.0 and over. Earlier versions ignored writes of duplicate
// events, while 5.0 started returning -EEXIST when a uprobe event already
// exists.
func TestUprobeCreateTraceFS(t *testing.T) {
	testutils.SkipOnOldKernel(t, "5.0", "<tracefs>/uprobe_events doesn't reject duplicate events")

	c := qt.New(t)

	// Fetch the offset from the /bin/bash Executable already defined.
	off, err := bashEx.offset(bashSym, &UprobeOptions{})
	c.Assert(err, qt.IsNil)

	// Sanitize the symbol in order to be used in tracefs API.
	ssym := sanitizeSymbol(bashSym)

	pg, _ := randomGroup("ebpftest")
	rg, _ := randomGroup("ebpftest")

	// Tee up cleanups in case any of the Asserts abort the function.
	defer func() {
		_ = closeTraceFSProbeEvent(uprobeType, pg, ssym)
		_ = closeTraceFSProbeEvent(uprobeType, rg, ssym)
	}()

	// Prepare probe args.
	args := probeArgs{
		group:  pg,
		symbol: ssym,
		path:   bashEx.path,
		offset: off,
	}

	// Create a uprobe.
	err = createTraceFSProbeEvent(uprobeType, args)
	c.Assert(err, qt.IsNil)

	// Attempt to create an identical uprobe using tracefs,
	// expect it to fail with os.ErrExist.
	err = createTraceFSProbeEvent(uprobeType, args)
	c.Assert(errors.Is(err, os.ErrExist), qt.IsTrue,
		qt.Commentf("expected consecutive uprobe creation to contain os.ErrExist, got: %v", err))

	// Expect a successful close of the kprobe.
	c.Assert(closeTraceFSProbeEvent(uprobeType, pg, ssym), qt.IsNil)

	args.group = rg
	args.ret = true

	// Same test for a kretprobe.
	err = createTraceFSProbeEvent(uprobeType, args)
	c.Assert(err, qt.IsNil)

	err = createTraceFSProbeEvent(uprobeType, args)
	c.Assert(os.IsExist(err), qt.IsFalse,
		qt.Commentf("expected consecutive uretprobe creation to contain os.ErrExist, got: %v", err))

	// Expect a successful close of the uretprobe.
	c.Assert(closeTraceFSProbeEvent(uprobeType, rg, ssym), qt.IsNil)
}

func TestUprobeSanitizedSymbol(t *testing.T) {
	tests := []struct {
		symbol   string
		expected string
	}{
		{"readline", "readline"},
		{"main.Func123", "main_Func123"},
		{"a.....a", "a_a"},
		{"./;'{}[]a", "_a"},
		{"***xx**xx###", "_xx_xx_"},
		{`@P#r$i%v^3*+t)i&k++--`, "_P_r_i_v_3_t_i_k_"},
	}

	for i, tt := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			sanitized := sanitizeSymbol(tt.symbol)
			if tt.expected != sanitized {
				t.Errorf("Expected sanitized symbol to be '%s', got '%s'", tt.expected, sanitized)
			}
		})
	}
}

func TestUprobeToken(t *testing.T) {
	tests := []struct {
		args     probeArgs
		expected string
	}{
		{probeArgs{path: "/bin/bash"}, "/bin/bash:0x0"},
		{probeArgs{path: "/bin/bash", offset: 1}, "/bin/bash:0x1"},
		{probeArgs{path: "/bin/bash", offset: 65535}, "/bin/bash:0xffff"},
		{probeArgs{path: "/bin/bash", offset: 65536}, "/bin/bash:0x10000"},
		{probeArgs{path: "/bin/bash", offset: 1, refCtrOffset: 1}, "/bin/bash:0x1(0x1)"},
		{probeArgs{path: "/bin/bash", offset: 1, refCtrOffset: 65535}, "/bin/bash:0x1(0xffff)"},
	}

	for i, tt := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			po := uprobeToken(tt.args)
			if tt.expected != po {
				t.Errorf("Expected path:offset to be '%s', got '%s'", tt.expected, po)
			}
		})
	}
}

func TestUprobeProgramCall(t *testing.T) {
	tests := []struct {
		name string
		elf  string
		args []string
		sym  string
	}{
		{
			"bash",
			"/bin/bash",
			[]string{"--help"},
			"main",
		},
		{
			"go-binary",
			path.Join(build.Default.GOROOT, "bin/go"),
			[]string{"version"},
			"main.main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "go-binary" {
				// https://github.com/cilium/ebpf/issues/406
				testutils.SkipOnOldKernel(t, "4.14", "uprobes on Go binaries silently fail on kernel < 4.14")
			}

			m, p := newUpdaterMapProg(t, ebpf.Kprobe)

			// Load the executable.
			ex, err := OpenExecutable(tt.elf)
			if err != nil {
				t.Fatal(err)
			}

			// Open Uprobe on the executable for the given symbol
			// and attach it to the ebpf program created above.
			u, err := ex.Uprobe(tt.sym, p, nil)
			if errors.Is(err, ErrNoSymbol) {
				// Assume bash::main and go::main.main always exists
				// and skip the test if the symbol can't be found as
				// certain OS (eg. Debian) strip binaries.
				t.Skipf("executable %s appear to be stripped, skipping", tt.elf)
			}
			if err != nil {
				t.Fatal(err)
			}

			// Trigger ebpf program call.
			trigger := func(t *testing.T) {
				if err := exec.Command(tt.elf, tt.args...).Run(); err != nil {
					t.Fatal(err)
				}
			}
			trigger(t)

			// Assert that the value at index 0 has been updated to 1.
			assertMapValue(t, m, 0, 1)

			// Detach the Uprobe.
			if err := u.Close(); err != nil {
				t.Fatal(err)
			}

			// Reset map value to 0 at index 0.
			if err := m.Update(uint32(0), uint32(0), ebpf.UpdateExist); err != nil {
				t.Fatal(err)
			}

			// Retrigger the ebpf program call.
			trigger(t)

			// Assert that this time the value has not been updated.
			assertMapValue(t, m, 0, 0)
		})
	}
}

func TestUprobeProgramWrongPID(t *testing.T) {
	m, p := newUpdaterMapProg(t, ebpf.Kprobe)

	// Load the '/bin/bash' executable.
	ex, err := OpenExecutable("/bin/bash")
	if err != nil {
		t.Fatal(err)
	}

	// Open Uprobe on '/bin/bash' for the symbol 'main'
	// and attach it to the ebpf program created above.
	// Create the perf-event with the current process' PID
	// to make sure the event is not fired when we will try
	// to trigger the program execution via exec.
	u, err := ex.Uprobe("main", p, &UprobeOptions{PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()

	// Trigger ebpf program call.
	if err := exec.Command("/bin/bash", "--help").Run(); err != nil {
		t.Fatal(err)
	}

	// Assert that the value at index 0 is still 0.
	assertMapValue(t, m, 0, 0)
}

func TestHaveRefCtrOffsetPMU(t *testing.T) {
	testutils.CheckFeatureTest(t, haveRefCtrOffsetPMU)
}
