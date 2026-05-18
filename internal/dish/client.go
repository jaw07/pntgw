package dish

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jhump/protoreflect/dynamic"
	"github.com/jhump/protoreflect/dynamic/grpcdynamic"
	"github.com/jhump/protoreflect/grpcreflect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	reflectpb "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
)

// PNT is a snapshot of what the dish reports at one moment.
type PNT struct {
	Time         time.Time
	Valid        bool
	Lat          float64
	Lon          float64
	AltMeters    float64
	UncertaintyM float64
	Sats         int
	InhibitGPS   bool
	// Dish-internal info
	HardwareVersion string
	SoftwareVersion string
}

type Client struct {
	addr   string
	conn   *grpc.ClientConn
	stub   grpcdynamic.Stub
	rc     *grpcreflect.Client
	cancel context.CancelFunc
}

func Dial(ctx context.Context, addr string) (*Client, error) {
	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	// Reflection client must use a long-lived context; conn lifecycle
	// (and Close) is what tears it down, not the dial timeout.
	bgCtx, cancel := context.WithCancel(context.Background())
	rc := grpcreflect.NewClient(metadata.NewOutgoingContext(bgCtx, metadata.MD{}), reflectpb.NewServerReflectionClient(conn))
	return &Client{
		addr:   addr,
		conn:   conn,
		stub:   grpcdynamic.NewStub(conn),
		rc:     rc,
		cancel: cancel,
	}, nil
}

func (c *Client) Close() error {
	if c.rc != nil {
		c.rc.Reset()
	}
	if c.cancel != nil {
		c.cancel()
	}
	return c.conn.Close()
}

// Poll grabs the dish's get_status + get_diagnostics and returns a PNT snapshot.
// Uses gRPC reflection so we don't need to embed SpaceX proto files.
func (c *Client) Poll(ctx context.Context) (*PNT, error) {
	method, err := c.rc.ResolveService("SpaceX.API.Device.Device")
	if err != nil {
		return nil, fmt.Errorf("resolve service: %w", err)
	}
	handle := method.FindMethodByName("Handle")
	if handle == nil {
		return nil, errors.New("Handle method not found")
	}
	requestDesc := handle.GetInputType()
	responseDesc := handle.GetOutputType()

	statusReq := dynamic.NewMessage(requestDesc)
	if err := setOneof(statusReq, "get_status", requestDesc, "GetStatusRequest"); err != nil {
		return nil, fmt.Errorf("build get_status: %w", err)
	}

	statusRespMsg, err := c.stub.InvokeRpc(ctx, handle, statusReq)
	if err != nil {
		return nil, fmt.Errorf("invoke get_status: %w", err)
	}
	statusResp, ok := statusRespMsg.(*dynamic.Message)
	if !ok {
		return nil, errors.New("status response not dynamic.Message")
	}
	_ = responseDesc

	diagReq := dynamic.NewMessage(requestDesc)
	if err := setOneof(diagReq, "get_diagnostics", requestDesc, "GetDiagnosticsRequest"); err != nil {
		return nil, fmt.Errorf("build get_diagnostics: %w", err)
	}
	diagRespMsg, err := c.stub.InvokeRpc(ctx, handle, diagReq)
	if err != nil {
		return nil, fmt.Errorf("invoke get_diagnostics: %w", err)
	}
	diagResp, _ := diagRespMsg.(*dynamic.Message)

	p := &PNT{Time: time.Now().UTC()}

	// status: dishGetStatus.gpsStats + deviceInfo
	if dgs := getMsg(statusResp, "dish_get_status"); dgs != nil {
		if dev := getMsg(dgs, "device_info"); dev != nil {
			p.HardwareVersion, _ = getString(dev, "hardware_version")
			p.SoftwareVersion, _ = getString(dev, "software_version")
		}
		if gps := getMsg(dgs, "gps_stats"); gps != nil {
			p.Valid, _ = getBool(gps, "gps_valid")
			if n, ok := getInt32(gps, "gps_sats"); ok {
				p.Sats = int(n)
			}
			p.InhibitGPS, _ = getBool(gps, "inhibit_gps")
		}
	}

	// diagnostics: dishGetDiagnostics.location
	if dgd := getMsg(diagResp, "dish_get_diagnostics"); dgd != nil {
		if loc := getMsg(dgd, "location"); loc != nil {
			lat, _ := getFloat64(loc, "latitude")
			lon, _ := getFloat64(loc, "longitude")
			alt, _ := getFloat64(loc, "altitude_meters")
			unc, _ := getFloat64(loc, "uncertainty_meters")
			p.Lat = lat
			p.Lon = lon
			p.AltMeters = alt
			p.UncertaintyM = unc
		}
	}

	return p, nil
}

// SetInhibitGPS opens a short-lived connection to the dish and issues
// dish_inhibit_gps. inhibit=true switches the terminal to its non-GPS
// (Starshield LEO) PNT source; false returns it to GPS. The dish holds
// this as runtime-only state — it clears on a dish/router reboot.
//
// Deliberately standalone (not on the polling client) so the web layer
// can call it without sharing the poll loop's connection. It is only
// ever invoked on an explicit user action.
func SetInhibitGPS(ctx context.Context, addr string, inhibit bool) error {
	c, err := Dial(ctx, addr)
	if err != nil {
		return err
	}
	defer c.Close()

	svc, err := c.rc.ResolveService("SpaceX.API.Device.Device")
	if err != nil {
		return fmt.Errorf("resolve service: %w", err)
	}
	handle := svc.FindMethodByName("Handle")
	if handle == nil {
		return errors.New("Handle method not found")
	}
	reqDesc := handle.GetInputType()

	req := dynamic.NewMessage(reqDesc)
	fd := reqDesc.FindFieldByName("dish_inhibit_gps")
	if fd == nil || fd.GetMessageType() == nil {
		return errors.New("dish_inhibit_gps not in Request schema")
	}
	sub := dynamic.NewMessage(fd.GetMessageType())
	inhibitFD := sub.GetMessageDescriptor().FindFieldByName("inhibit_gps")
	if inhibitFD == nil {
		return errors.New("inhibit_gps field not found")
	}
	if err := sub.TrySetField(inhibitFD, inhibit); err != nil {
		return fmt.Errorf("set inhibit_gps: %w", err)
	}
	if err := req.TrySetField(fd, sub); err != nil {
		return fmt.Errorf("set dish_inhibit_gps: %w", err)
	}
	if _, err := c.stub.InvokeRpc(ctx, handle, req); err != nil {
		return fmt.Errorf("invoke dish_inhibit_gps: %w", err)
	}
	return nil
}

// setOneof sets a oneof request field to an empty sub-message of the named type.
func setOneof(msg *dynamic.Message, fieldName string, parentDesc interface{}, _ string) error {
	fd := msg.GetMessageDescriptor().FindFieldByName(fieldName)
	if fd == nil {
		return fmt.Errorf("field %s not found in Request", fieldName)
	}
	if fd.GetMessageType() == nil {
		return fmt.Errorf("field %s is not a message", fieldName)
	}
	sub := dynamic.NewMessage(fd.GetMessageType())
	return msg.TrySetField(fd, sub)
}

func getMsg(m *dynamic.Message, name string) *dynamic.Message {
	if m == nil {
		return nil
	}
	fd := m.GetMessageDescriptor().FindFieldByName(name)
	if fd == nil {
		return nil
	}
	v, err := m.TryGetField(fd)
	if err != nil || v == nil {
		return nil
	}
	sub, _ := v.(*dynamic.Message)
	return sub
}

func getString(m *dynamic.Message, name string) (string, bool) {
	v, err := m.TryGetFieldByName(name)
	if err != nil || v == nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func getBool(m *dynamic.Message, name string) (bool, bool) {
	v, err := m.TryGetFieldByName(name)
	if err != nil || v == nil {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func getInt32(m *dynamic.Message, name string) (int32, bool) {
	v, err := m.TryGetFieldByName(name)
	if err != nil || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case int32:
		return n, true
	case uint32:
		return int32(n), true
	case int64:
		return int32(n), true
	}
	return 0, false
}

func getFloat64(m *dynamic.Message, name string) (float64, bool) {
	v, err := m.TryGetFieldByName(name)
	if err != nil || v == nil {
		return 0, false
	}
	switch f := v.(type) {
	case float64:
		return f, true
	case float32:
		return float64(f), true
	}
	return 0, false
}
