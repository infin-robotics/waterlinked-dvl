package dvl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const probeTargetHeader = "X-Probe-Target"

// reroute sends every request to server while recording the host the client
// meant to reach in probeTargetHeader, so tests exercise the real HTTP client
// without touching any network but loopback.
type reroute struct {
	base   http.RoundTripper
	server string

	// inFlight, when set, counts requests that have not yet returned.
	inFlight *atomic.Int32
}

func (r reroute) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.inFlight != nil {
		r.inFlight.Add(1)
		defer r.inFlight.Add(-1)
	}
	clone := req.Clone(req.Context())
	clone.Header.Set(probeTargetHeader, req.URL.Host)
	clone.URL.Host = r.server
	return r.base.RoundTrip(clone)
}

func testClient(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	return countingClient(t, handler, nil)
}

func countingClient(t *testing.T, handler http.HandlerFunc, inFlight *atomic.Int32) *http.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	base := &http.Transport{DisableKeepAlives: true}
	t.Cleanup(base.CloseIdleConnections)
	return newDiscoveryClient(reroute{base: base, server: server.Listener.Addr().String(), inFlight: inFlight})
}

const dvlAbout = `{"product_name":"DVL A50","version":"1"}`

// dvlAt answers as a DVL on the listed targets and 404s elsewhere.
func dvlAt(targets ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for _, target := range targets {
			if r.Header.Get(probeTargetHeader) == target && r.URL.Path == "/api/v1/about" {
				fmt.Fprint(w, dvlAbout)
				return
			}
		}
		http.NotFound(w, r)
	}
}

func TestDiscoveryReadsDeviceIdentity(t *testing.T) {
	t.Parallel()

	const response = `{"chipid":"0xf14002310c0215","hardware_revision":4,"is_ready":true,"product_id":21035,"product_name":"DVL A50","variant":"performance","version":"2.7.2 (build metadata)","version_short":"2.7.2"}`
	want := Device{
		Addr:                 netip.MustParseAddr("10.0.0.1"),
		ChipID:               "0xf14002310c0215",
		HardwareRevision:     4,
		ProductID:            21035,
		ProductName:          "DVL A50",
		Variant:              "performance",
		FirmwareVersion:      "2.7.2 (build metadata)",
		FirmwareVersionShort: "2.7.2",
		Ready:                true,
	}
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, response)
	})
	prefix := netip.MustParsePrefix("10.0.0.1/32")
	first, err := discoverFirst(context.Background(), client, prefix)
	if err != nil || first != want {
		t.Fatalf("first device = %+v, %v; want %+v", first, err, want)
	}
	all, err := scan(context.Background(), client, prefix, false)
	if err != nil || len(all) != 1 || all[0] != want {
		t.Fatalf("all devices = %+v, %v; want [%+v]", all, err, want)
	}
}

func TestScanFindsDevicesSortedByAddress(t *testing.T) {
	t.Parallel()

	client := testClient(t, dvlAt("10.1.2.200:80", "10.1.2.7:80", "10.1.2.90:80"))
	devices, err := scan(context.Background(), client, netip.MustParsePrefix("10.1.2.0/24"), false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range devices {
		if d.ProductName != "DVL A50" {
			t.Errorf("ProductName = %q", d.ProductName)
		}
		got = append(got, d.Addr.String())
	}
	if want := "10.1.2.7 10.1.2.90 10.1.2.200"; strings.Join(got, " ") != want {
		t.Fatalf("devices = %v, want %s", got, want)
	}
}

func TestScanEmptyNetworkSucceeds(t *testing.T) {
	t.Parallel()

	client := testClient(t, http.NotFound)
	devices, err := scan(context.Background(), client, netip.MustParsePrefix("10.1.2.0/30"), false)
	if err != nil || len(devices) != 0 {
		t.Fatalf("devices, err = %v, %v", devices, err)
	}
}

func TestScanMasksHostBitsAndHandlesTinyPrefixes(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	seen := map[string]bool{}
	handler := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Header.Get(probeTargetHeader)] = true
		mu.Unlock()
		http.NotFound(w, r)
	}
	for _, tc := range []struct {
		prefix string
		want   []string
	}{
		{"10.0.0.5/32", []string{"10.0.0.5:80"}},
		{"10.0.0.5/31", []string{"10.0.0.4:80", "10.0.0.5:80"}},
		{"10.0.0.6/30", []string{"10.0.0.5:80", "10.0.0.6:80"}},
	} {
		clear(seen)
		client := testClient(t, handler)
		if _, err := scan(context.Background(), client, netip.MustParsePrefix(tc.prefix), false); err != nil {
			t.Fatalf("%s: %v", tc.prefix, err)
		}
		if len(seen) != len(tc.want) {
			t.Errorf("%s: probed %v, want %v", tc.prefix, seen, tc.want)
		}
		for _, target := range tc.want {
			if !seen[target] {
				t.Errorf("%s: %s not probed (probed %v)", tc.prefix, target, seen)
			}
		}
	}
}

func TestScanRejectsPrefixesBeforeAnyNetworkActivity(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	client := testClient(t, func(http.ResponseWriter, *http.Request) { requests.Add(1) })
	for _, prefix := range []netip.Prefix{
		{},
		netip.MustParsePrefix("10.0.0.0/15"),
		netip.MustParsePrefix("0.0.0.0/0"),
		netip.MustParsePrefix("fd00::/120"),
		netip.MustParsePrefix("::ffff:10.0.0.0/120"),
	} {
		if _, err := scan(context.Background(), client, prefix, false); err == nil {
			t.Errorf("scan(%v) succeeded", prefix)
		}
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("%d requests sent for rejected prefixes", n)
	}
	if _, err := scan(nil, client, netip.MustParsePrefix("10.0.0.0/24"), false); !errors.Is(err, errNilContext) { //nolint:staticcheck // nil context is the case under test
		t.Fatalf("nil context error = %v", err)
	}
}

func TestScanAcceptsSlash16(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := scan(ctx, testClient(t, http.NotFound), netip.MustParsePrefix("10.0.0.0/16"), false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestProbeTreatsAnythingButADVLAboutResponseAsAMiss(t *testing.T) {
	t.Parallel()

	oversized := `{"product_name":"DVL A50","pad":"` + strings.Repeat("x", maximumAboutSize) + `"}`
	for name, respond := range map[string]http.HandlerFunc{
		"wrong product":    func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"product_name":"Router"}`) },
		"no product":       func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{}`) },
		"not json":         func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `DVL`) },
		"json array":       func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `["DVL"]`) },
		"wrong type":       func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"product_name":7}`) },
		"error status":     func(w http.ResponseWriter, _ *http.Request) { http.Error(w, dvlAbout, 500) },
		"empty body":       func(http.ResponseWriter, *http.Request) {},
		"oversized body":   func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, oversized) },
		"connection reset": func(w http.ResponseWriter, _ *http.Request) { panic(http.ErrAbortHandler) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			devices, err := scan(context.Background(), testClient(t, respond), netip.MustParsePrefix("10.0.0.1/32"), false)
			if err != nil || len(devices) != 0 {
				t.Fatalf("devices, err = %v, %v", devices, err)
			}
		})
	}
}

func TestScanDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	var paths []string
	var mu sync.Mutex
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/api/v1/about" {
			http.Redirect(w, r, "http://192.0.2.1/elsewhere", http.StatusFound)
			return
		}
		fmt.Fprint(w, dvlAbout)
	})
	devices, err := scan(context.Background(), client, netip.MustParsePrefix("10.0.0.1/32"), false)
	if err != nil || len(devices) != 0 {
		t.Fatalf("devices, err = %v, %v", devices, err)
	}
	if len(paths) != 1 {
		t.Fatalf("requests = %v, want only the original", paths)
	}
}

func TestDiscoveryClientIgnoresProxyEnvironment(t *testing.T) {
	t.Parallel()

	transport, ok := newDiscoveryClient(nil).Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatalf("transport = %#v, want an http.Transport without a proxy", newDiscoveryClient(nil).Transport)
	}
}

func TestScanBoundsConcurrentProbes(t *testing.T) {
	t.Parallel()

	var inFlight, peak atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	})
	if _, err := scan(context.Background(), client, netip.MustParsePrefix("10.0.0.0/24"), false); err != nil {
		t.Fatal(err)
	}
	if p := peak.Load(); p < 2 || p > probeConcurrency {
		t.Fatalf("peak in-flight probes = %d, want 2..%d", p, probeConcurrency)
	}
}

func TestScanTimesOutSlowHosts(t *testing.T) {
	t.Parallel()

	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(probeTargetHeader) == "10.0.0.1:80" {
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, dvlAbout)
	})
	start := time.Now()
	devices, err := scan(context.Background(), client, netip.MustParsePrefix("10.0.0.0/30"), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Addr != netip.MustParseAddr("10.0.0.2") {
		t.Fatalf("devices = %v", devices)
	}
	if elapsed := time.Since(start); elapsed > 3*probeTimeout {
		t.Fatalf("scan took %s with a %s probe timeout", elapsed, probeTimeout)
	}
}

func TestScanCancellationReturnsPartialResults(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan struct{})
	go func() {
		<-served
		time.Sleep(100 * time.Millisecond) // let the device reach the collector
		cancel()
	}()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(probeTargetHeader) == "10.0.0.1:80" {
			fmt.Fprint(w, dvlAbout)
			close(served)
			return
		}
		<-r.Context().Done()
	})
	devices, err := scan(ctx, client, netip.MustParsePrefix("10.0.0.0/30"), false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(devices) != 1 || devices[0].Addr != netip.MustParseAddr("10.0.0.1") {
		t.Fatalf("devices = %v, want the one found before cancellation", devices)
	}
}

func TestScanFirstMatchAbandonsPendingProbesBeforeReturning(t *testing.T) {
	t.Parallel()

	var inFlight atomic.Int32
	client := countingClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(probeTargetHeader) == "10.0.0.1:80" {
			fmt.Fprint(w, dvlAbout)
			return
		}
		<-r.Context().Done() // hang until the client abandons the request
	}, &inFlight)
	devices, err := scan(context.Background(), client, netip.MustParsePrefix("10.0.0.0/24"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Addr != netip.MustParseAddr("10.0.0.1") {
		t.Fatalf("devices = %v", devices)
	}
	if n := inFlight.Load(); n != 0 {
		t.Fatalf("%d probes still running after scan returned", n)
	}
}

func TestDiscoverReportsNoDevicesAndCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Discover(ctx, netip.MustParsePrefix("192.0.2.0/24")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Discover err = %v, want context.Canceled", err)
	}
	if _, err := DiscoverAll(ctx, netip.MustParsePrefix("192.0.2.0/24")); !errors.Is(err, context.Canceled) {
		t.Fatalf("DiscoverAll err = %v, want context.Canceled", err)
	}
	if _, err := Discover(nil, netip.MustParsePrefix("192.0.2.0/24")); !errors.Is(err, errNilContext) { //nolint:staticcheck // nil context is the case under test
		t.Fatalf("Discover nil ctx err = %v", err)
	}
	if _, err := DiscoverAll(nil, netip.MustParsePrefix("192.0.2.0/24")); !errors.Is(err, errNilContext) { //nolint:staticcheck // nil context is the case under test
		t.Fatalf("DiscoverAll nil ctx err = %v", err)
	}
}

func TestDiscoverMapsEmptyScanToErrNoDevices(t *testing.T) {
	t.Parallel()

	client := testClient(t, http.NotFound)
	if _, err := discoverFirst(context.Background(), client, netip.MustParsePrefix("10.0.0.0/30")); !errors.Is(err, ErrNoDevices) {
		t.Fatalf("err = %v, want ErrNoDevices", err)
	}
	client = testClient(t, dvlAt("10.0.0.2:80"))
	device, err := discoverFirst(context.Background(), client, netip.MustParsePrefix("10.0.0.0/30"))
	if err != nil || device.Addr != netip.MustParseAddr("10.0.0.2") {
		t.Fatalf("device, err = %v, %v", device, err)
	}
}
