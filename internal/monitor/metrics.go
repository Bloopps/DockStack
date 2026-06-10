package monitor

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Metrics struct {
	CPUPercent   int
	CPUCores     []int
	NCores       int
	MemUsedGB    float64
	MemTotalGB   float64
	MemPercent   int
	SwapUsedGB   float64
	SwapTotalGB  float64
	SwapPercent  int
	DiskUsedGB   int
	DiskTotalGB  int
	DiskPercent  int
	DiskReadKBs  int
	DiskWriteKBs int
	Load1        string
	Load5        string
	Load15       string
	Uptime       string
}

type collector struct {
	// aggregate CPU
	prevCPUIdle  uint64
	prevCPUTotal uint64
	// per-core CPU
	prevCoreIdle  []uint64
	prevCoreTotal []uint64
	// disk I/O
	prevDiskR  uint64
	prevDiskW  uint64
	prevDiskAt time.Time
}

var c collector

func parseUint(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}

func Collect() Metrics {
	var m Metrics
	c.cpu(&m)
	memswap(&m)
	disk(&m)
	c.diskIO(&m)
	load(&m)
	uptime(&m)
	return m
}

func (col *collector) cpu(m *Metrics) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return
	}
	defer f.Close()

	var aggIdle, aggTotal uint64
	var coreIdle, coreTotal []uint64

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		name := fields[0]
		// fields[4] is idle; total = user..steal (fields[1:9]). We stop at steal on
		// purpose: guest/guest_nice are already folded into user/nice by the kernel,
		// so summing them would double-count. strconv is far cheaper than fmt.Sscan
		// in this per-core loop that runs every 2s.
		idle := parseUint(fields[4])
		var total uint64
		end := len(fields)
		if end > 9 {
			end = 9
		}
		for _, f := range fields[1:end] {
			total += parseUint(f)
		}

		if name == "cpu" {
			aggIdle = idle
			aggTotal = total
		} else if strings.HasPrefix(name, "cpu") && len(name) > 3 {
			coreIdle = append(coreIdle, idle)
			coreTotal = append(coreTotal, total)
		}
	}

	// Aggregate
	dt := aggTotal - col.prevCPUTotal
	di := aggIdle - col.prevCPUIdle
	col.prevCPUIdle = aggIdle
	col.prevCPUTotal = aggTotal
	if dt > 0 {
		pct := int((dt - di) * 100 / dt)
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		m.CPUPercent = pct
	}

	// Per-core
	n := len(coreIdle)
	m.NCores = n
	if len(col.prevCoreIdle) != n {
		col.prevCoreIdle = make([]uint64, n)
		col.prevCoreTotal = make([]uint64, n)
	}
	m.CPUCores = make([]int, n)
	for i := 0; i < n; i++ {
		cdt := coreTotal[i] - col.prevCoreTotal[i]
		cdi := coreIdle[i] - col.prevCoreIdle[i]
		col.prevCoreIdle[i] = coreIdle[i]
		col.prevCoreTotal[i] = coreTotal[i]
		if cdt > 0 {
			pct := int((cdt - cdi) * 100 / cdt)
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			m.CPUCores[i] = pct
		}
	}
}

func memswap(m *Metrics) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()

	vals := map[string]uint64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 {
			v, _ := strconv.ParseUint(fields[1], 10, 64)
			vals[strings.TrimSuffix(fields[0], ":")] = v
		}
	}
	mt := vals["MemTotal"]
	ma := vals["MemAvailable"]
	st := vals["SwapTotal"]
	sf := vals["SwapFree"]

	used := mt - ma
	m.MemUsedGB = float64(used) / 1048576
	m.MemTotalGB = float64(mt) / 1048576
	if mt > 0 {
		m.MemPercent = int(used * 100 / mt)
	}
	swapUsed := st - sf
	m.SwapUsedGB = float64(swapUsed) / 1048576
	m.SwapTotalGB = float64(st) / 1048576
	if st > 0 {
		m.SwapPercent = int(swapUsed * 100 / st)
	}
}

func disk(m *Metrics) {
	var stat syscallStatfs
	if err := statfs("/", &stat); err != nil {
		return
	}
	total := stat.Blocks * uint64(stat.Bsize)
	avail := stat.Bavail * uint64(stat.Bsize)
	used := total - avail
	m.DiskUsedGB = int(used / (1024 * 1024 * 1024))
	m.DiskTotalGB = int(total / (1024 * 1024 * 1024))
	if total > 0 {
		m.DiskPercent = int(used * 100 / total)
	}
}

func (col *collector) diskIO(m *Metrics) {
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return
	}
	defer f.Close()

	var rSec, wSec uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		name := fields[2]
		if !isPhysicalDisk(name) {
			continue
		}
		r, _ := strconv.ParseUint(fields[5], 10, 64)
		w, _ := strconv.ParseUint(fields[9], 10, 64)
		rSec += r
		wSec += w
	}

	// Horloge monotone en fractions de seconde : avec un dt arrondi à la
	// seconde entière, le jitter du tick de 2 s (dt mesuré à 1 ou 3) fausse
	// les débits jusqu'à un facteur 2.
	now := time.Now()
	if !col.prevDiskAt.IsZero() {
		if secs := now.Sub(col.prevDiskAt).Seconds(); secs > 0 {
			dr := int64(rSec) - int64(col.prevDiskR)
			dw := int64(wSec) - int64(col.prevDiskW)
			if dr < 0 {
				dr = 0
			}
			if dw < 0 {
				dw = 0
			}
			m.DiskReadKBs = int(float64(dr) * 512 / 1024 / secs)
			m.DiskWriteKBs = int(float64(dw) * 512 / 1024 / secs)
		}
	}
	col.prevDiskR = rSec
	col.prevDiskW = wSec
	col.prevDiskAt = now
}

func isPhysicalDisk(name string) bool {
	if len(name) < 3 {
		return false
	}
	prefixes := []string{"sd", "vd", "hd"}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) && len(name) == len(p)+1 {
			return true
		}
	}
	// nvme0n1 — exactly 7 chars like nvme0n1
	if strings.HasPrefix(name, "nvme") && len(name) == 7 {
		return true
	}
	// mmcblk0
	if strings.HasPrefix(name, "mmcblk") && len(name) == 7 {
		return true
	}
	return false
}

func load(m *Metrics) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return
	}
	fields := strings.Fields(string(data))
	if len(fields) >= 3 {
		m.Load1, m.Load5, m.Load15 = fields[0], fields[1], fields[2]
	}
}

func uptime(m *Metrics) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return
	}
	f, _ := strconv.ParseFloat(fields[0], 64)
	sec := int(f)
	d, h, min := sec/86400, (sec%86400)/3600, (sec%3600)/60
	switch {
	case d > 0:
		m.Uptime = fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		m.Uptime = fmt.Sprintf("%dh %dm", h, min)
	default:
		m.Uptime = fmt.Sprintf("%dm", min)
	}
}
