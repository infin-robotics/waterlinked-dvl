package dvl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// discoveryPort is the DVL's HTTP port. It is distinct from DefaultPort,
	// which is the TCP JSON port.
	discoveryPort = 80

	// minimumDiscoveryBits bounds a scan to 65,534 hosts.
	minimumDiscoveryBits = 16

	probeTimeout     = time.Second
	probeConcurrency = 32
	maximumAboutSize = 64 << 10
)

// ErrNoDevices is returned by [Discover] when a complete scan finds no DVL.
var ErrNoDevices = errors.New("dvl: no devices found")

// Device is a DVL found by [Discover] or [DiscoverAll]. Identity fields use
// the same names as [DeviceInfo], but describe the HTTP discovery response.
// Fields absent from that response have their zero values.
type Device struct {
	// Addr is the device's IPv4 address.
	Addr netip.Addr

	// ChipID is the device's unique hardware identifier.
	ChipID string

	// HardwareRevision is the board revision.
	HardwareRevision int

	// ProductID is the numeric product code.
	ProductID int

	// ProductName is the model name, such as "DVL A50".
	ProductName string

	// Variant distinguishes builds of one model, such as "performance".
	Variant string

	// FirmwareVersion includes build metadata; FirmwareVersionShort is the
	// release number alone, such as "2.7.2".
	FirmwareVersion      string
	FirmwareVersionShort string

	// Ready reports whether the device had finished starting up when probed.
	// It does not indicate bottom lock.
	Ready bool
}

// Discover scans prefix for a DVL and returns the first one that answers.
//
// Discovery follows the Blue Robotics BlueOS extension: it sends GET /api/v1/about to
// port 80 of every host in prefix and accepts a successful response whose JSON
// object has a product_name containing "DVL". Hosts that refuse, time out,
// answer with anything else, or send more than 64 KiB are skipped. No data
// connection is opened; to connect to the result, pass the address with
// [DefaultPort] to [Dialer.Dial]:
//
//	device, err := dvl.Discover(ctx, netip.MustParsePrefix("192.168.194.0/24"))
//	if err != nil {
//		return err
//	}
//	conn, err := dvl.Dial(ctx, netip.AddrPortFrom(device.Addr, dvl.DefaultPort).String())
//
// "First" means the first host to answer, not the lowest address, so with
// several DVLs on prefix the result varies between calls. Use [DiscoverAll] to
// see them all. A scan that finds nothing returns [ErrNoDevices]. If ctx ends
// first, Discover returns its error.
//
// Only IPv4 prefixes of /16 or narrower are accepted; anything else, including
// an IPv4-mapped IPv6 prefix, is rejected before any network activity. Host
// bits in prefix are ignored. For a /30 or wider, the network and broadcast
// addresses are skipped. Nothing outside prefix is probed: there is no mDNS
// lookup, no fallback subnet, and HTTP redirects and proxy environment
// variables are ignored. Each host gets one second and up to 32 hosts are
// probed at once. All probes have ended when Discover returns.
func Discover(ctx context.Context, prefix netip.Prefix) (Device, error) {
	return discoverFirst(ctx, newDiscoveryClient(nil), prefix)
}

func discoverFirst(ctx context.Context, client *http.Client, prefix netip.Prefix) (Device, error) {
	devices, err := scan(ctx, client, prefix, true)
	if err != nil {
		return Device{}, err
	}
	if len(devices) == 0 {
		return Device{}, ErrNoDevices
	}
	return devices[0], nil
}

// DiscoverAll scans every host in prefix and returns the DVLs found, sorted by
// address. Probing, validation, and limits are those of [Discover].
//
// A scan that finds nothing succeeds with an empty result. If ctx ends first,
// DiscoverAll returns the devices found so far together with ctx's error.
func DiscoverAll(ctx context.Context, prefix netip.Prefix) ([]Device, error) {
	return scan(ctx, newDiscoveryClient(nil), prefix, false)
}

// newDiscoveryClient returns an HTTP client that talks to its target directly
// and never follows redirects, so a device cannot steer the scan outside the
// requested prefix. A nil transport selects a plain one that ignores the
// proxy environment.
func newDiscoveryClient(transport http.RoundTripper) *http.Client {
	if transport == nil {
		transport = &http.Transport{DisableKeepAlives: true}
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// scan probes every host in prefix. With firstOnly it stops at the first
// device found. It returns only after every probe has finished.
func scan(ctx context.Context, client *http.Client, prefix netip.Prefix, firstOnly bool) ([]Device, error) {
	if ctx == nil {
		return nil, errNilContext
	}
	hosts, err := discoveryHosts(prefix)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	probeCtx, stop := context.WithCancel(ctx)
	defer stop()

	jobs := make(chan netip.Addr)
	found := make(chan Device)
	var workers sync.WaitGroup
	for range min(probeConcurrency, len(hosts)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for addr := range jobs {
				if device, ok := probe(probeCtx, client, addr); ok {
					select {
					case found <- device:
					case <-probeCtx.Done():
					}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, addr := range hosts {
			select {
			case jobs <- addr:
			case <-probeCtx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(found)
	}()

	var devices []Device
	for device := range found {
		devices = append(devices, device)
		if firstOnly {
			stop()
			break
		}
	}
	// Wait for the workers: found closes only after all of them return.
	for range found {
	}

	if firstOnly && len(devices) > 0 {
		return devices, nil
	}
	slices.SortFunc(devices, func(a, b Device) int { return a.Addr.Compare(b.Addr) })
	return devices, ctx.Err()
}

// discoveryHosts lists the addresses to probe in prefix, validating it first.
func discoveryHosts(prefix netip.Prefix) ([]netip.Addr, error) {
	switch {
	case !prefix.IsValid():
		return nil, errors.New("dvl: invalid discovery prefix")
	case !prefix.Addr().Is4():
		return nil, fmt.Errorf("dvl: discovery prefix %s is not IPv4", prefix)
	case prefix.Bits() < minimumDiscoveryBits:
		return nil, fmt.Errorf("dvl: discovery prefix %s is wider than /%d", prefix, minimumDiscoveryBits)
	}

	prefix = prefix.Masked()
	var hosts []netip.Addr
	for addr := prefix.Addr(); prefix.Contains(addr); addr = addr.Next() {
		hosts = append(hosts, addr)
	}
	if prefix.Bits() < 31 {
		hosts = hosts[1 : len(hosts)-1]
	}
	return hosts, nil
}

// probe reports whether a DVL answers on addr. Every failure is a miss.
func probe(ctx context.Context, client *http.Client, addr netip.Addr) (Device, bool) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	url := "http://" + netip.AddrPortFrom(addr, discoveryPort).String() + "/api/v1/about"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Device{}, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return Device{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Device{}, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maximumAboutSize+1))
	if err != nil || len(body) > maximumAboutSize {
		return Device{}, false
	}
	var about struct {
		ChipID               string `json:"chipid"`
		HardwareRevision     int    `json:"hardware_revision"`
		ProductID            int    `json:"product_id"`
		ProductName          string `json:"product_name"`
		Variant              string `json:"variant"`
		FirmwareVersion      string `json:"version"`
		FirmwareVersionShort string `json:"version_short"`
		Ready                bool   `json:"is_ready"`
	}
	if json.Unmarshal(body, &about) != nil || !strings.Contains(about.ProductName, "DVL") {
		return Device{}, false
	}
	return Device{
		Addr:                 addr,
		ChipID:               about.ChipID,
		HardwareRevision:     about.HardwareRevision,
		ProductID:            about.ProductID,
		ProductName:          about.ProductName,
		Variant:              about.Variant,
		FirmwareVersion:      about.FirmwareVersion,
		FirmwareVersionShort: about.FirmwareVersionShort,
		Ready:                about.Ready,
	}, true
}
