package nmea

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Fix translates a dish PNT snapshot into a slice of NMEA-0183 sentences.
// We emit GGA, RMC, ZDA, GSA — the standard quartet a typical NMEA consumer expects.
type Fix struct {
	Time         time.Time
	Valid        bool
	Lat          float64 // signed decimal degrees, + N / - S
	Lon          float64 // signed decimal degrees, + E / - W
	AltMeters    float64
	UncertaintyM float64
	Sats         int
}

// Sentences returns the four core NMEA sentences for this fix, each
// terminated with \r\n.
func Sentences(f Fix) []string {
	gga := buildGGA(f)
	rmc := buildRMC(f)
	zda := buildZDA(f)
	gsa := buildGSA(f)
	return []string{gga, rmc, zda, gsa}
}

// SentencesString joins Sentences() into one contiguous CRLF-terminated string.
func SentencesString(f Fix) string {
	return strings.Join(Sentences(f), "")
}

// $GPGGA,hhmmss.ss,llll.ll,a,yyyyy.yy,a,x,xx,x.x,x.x,M,x.x,M,x.x,xxxx*hh
func buildGGA(f Fix) string {
	hms := hhmmss(f.Time)
	lat, ns := decimalToDMM(f.Lat, true)
	lon, ew := decimalToDMM(f.Lon, false)
	fix := "0"
	if f.Valid {
		fix = "1"
	}
	hdop := 1.0
	if f.UncertaintyM > 0 {
		hdop = f.UncertaintyM / 5.0
	}
	body := fmt.Sprintf("GPGGA,%s,%s,%s,%s,%s,%s,%02d,%.1f,%.1f,M,0.0,M,,",
		hms, lat, ns, lon, ew, fix, f.Sats, hdop, f.AltMeters)
	return wrap(body)
}

// $GPRMC,hhmmss.ss,A,llll.ll,a,yyyyy.yy,a,x.x,x.x,ddmmyy,x.x,a*hh
func buildRMC(f Fix) string {
	hms := hhmmss(f.Time)
	dmy := ddmmyy(f.Time)
	lat, ns := decimalToDMM(f.Lat, true)
	lon, ew := decimalToDMM(f.Lon, false)
	status := "V"
	if f.Valid {
		status = "A"
	}
	body := fmt.Sprintf("GPRMC,%s,%s,%s,%s,%s,%s,0.0,0.0,%s,,,A",
		hms, status, lat, ns, lon, ew, dmy)
	return wrap(body)
}

// $GPZDA,hhmmss.ss,dd,mm,yyyy,xx,xx*hh
func buildZDA(f Fix) string {
	t := f.Time.UTC()
	body := fmt.Sprintf("GPZDA,%s,%02d,%02d,%04d,00,00",
		hhmmss(t), t.Day(), int(t.Month()), t.Year())
	return wrap(body)
}

// $GPGSA,A,3,...,PDOP,HDOP,VDOP*hh — we don't have per-sat PRNs from the dish, just count.
func buildGSA(f Fix) string {
	fixType := "1"
	if f.Valid {
		fixType = "3"
	}
	hdop := 1.0
	if f.UncertaintyM > 0 {
		hdop = f.UncertaintyM / 5.0
	}
	body := fmt.Sprintf("GPGSA,A,%s,,,,,,,,,,,,,%.1f,%.1f,%.1f", fixType, hdop, hdop, hdop)
	return wrap(body)
}

func hhmmss(t time.Time) string {
	t = t.UTC()
	return fmt.Sprintf("%02d%02d%02d.%02d", t.Hour(), t.Minute(), t.Second(), t.Nanosecond()/10_000_000)
}

func ddmmyy(t time.Time) string {
	t = t.UTC()
	return fmt.Sprintf("%02d%02d%02d", t.Day(), int(t.Month()), t.Year()%100)
}

// decimalToDMM converts signed decimal degrees to NMEA DMM (ddmm.mmmm) + hemisphere.
// isLat=true for latitude (NS), false for longitude (EW).
func decimalToDMM(deg float64, isLat bool) (string, string) {
	hemi := "N"
	if isLat {
		if deg < 0 {
			hemi = "S"
		}
	} else {
		hemi = "E"
		if deg < 0 {
			hemi = "W"
		}
	}
	abs := math.Abs(deg)
	d := math.Floor(abs)
	m := (abs - d) * 60
	width := 2
	if !isLat {
		width = 3
	}
	return fmt.Sprintf("%0*d%07.4f", width, int(d), m), hemi
}

// wrap prefixes "$", computes XOR checksum over the body, appends "*HH\r\n".
func wrap(body string) string {
	var cs byte
	for i := 0; i < len(body); i++ {
		cs ^= body[i]
	}
	return fmt.Sprintf("$%s*%02X\r\n", body, cs)
}
