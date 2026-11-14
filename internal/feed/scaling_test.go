package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

type largeDocument struct {
	name, prefix, unit, suffix string
	count                      int
}

var largeDocuments = []largeDocument{
	{"items", `<rss><channel><title>T</title>`, `<item/>`, `</channel></rss>`, 200000},
	{"chunks", `<rss><channel><item><description>`, `xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx<b/>`, `</description></item></channel></rss>`, 200000},
	{"media", `<rss xmlns:media="http://search.yahoo.com/mrss/" xmlns:dc="http://purl.org/dc/elements/1.1/"><channel>`, `<item><media:content url="http://e.test/a.png"/></item>`, `</channel></rss>`, 50000},
}

func (f largeDocument) document(n int) []byte {
	return []byte(f.prefix + strings.Repeat(f.unit, n) + f.suffix)
}

func checkLargeDocument(t *testing.T, fixture largeDocument, n int, data []byte) {
	t.Helper()
	f, err := Parse(data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := n
	if fixture.name == "chunks" {
		want = 1
	}
	if len(f.Items) != want {
		t.Fatalf("%d items, want %d", len(f.Items), want)
	}
	if fixture.name == "chunks" && f.Items[0].Content != strings.Repeat("x", 40*n) {
		t.Fatal("text chunks were lost or changed")
	}
	if fixture.name == "media" {
		for _, item := range []*Item{f.Items[0], f.Items[len(f.Items)-1]} {
			if len(item.Enclosures) != 1 || item.Enclosures[0].URL != "http://e.test/a.png" {
				t.Fatal("media enclosures were lost or changed")
			}
		}
	}
}

const scalingEnv = "FRESHGO_PARSE_SCALING_WORKER"
const scalingPrefix = "parse-scaling:"

type scalingMeasurement struct {
	Operations int
	Bytes      uint64
}

// A helper process gives each measurement its own CPU/allocator accounting.
// The normal suite invokes this test only through TestParseScalesLinearly.
func TestParseScalingWorker(t *testing.T) {
	request := os.Getenv(scalingEnv)
	if request == "" {
		return
	}
	parts := strings.Split(request, ",")
	if len(parts) != 3 {
		t.Fatal("invalid parse scaling input")
	}
	n, nErr := strconv.Atoi(parts[1])
	repeats, rErr := strconv.Atoi(parts[2])
	valid := n == 256 && repeats == 32 || n == 2048 && repeats == 4
	if parts[0] == "media" {
		valid = valid || n == 1024 && repeats == 32 || n == 8192 && repeats == 4
	}
	if nErr != nil || rErr != nil || !valid {
		t.Fatal("invalid parse scaling input")
	}
	var fixture *largeDocument
	for i := range largeDocuments {
		if largeDocuments[i].name == parts[0] {
			fixture = &largeDocuments[i]
		}
	}
	if fixture == nil {
		t.Fatal("invalid parse scaling input")
	}
	data := fixture.document(n)
	checkLargeDocument(t, *fixture, n, data) // warm one-time parser state
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range repeats {
		checkLargeDocument(t, *fixture, n, data)
	}
	runtime.ReadMemStats(&after)
	result, err := json.Marshal(scalingMeasurement{repeats, after.TotalAlloc - before.TotalAlloc})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", scalingPrefix, result)
}

// Eight times the work may use up to 24 times the CPU/allocated bytes: ample
// slack for linear/n-log-n growth, but well below quadratic's factor of 64.
// A process's CPU time excludes time waiting for a contended host CPU. The
// deadline is only a hung-worker safety net, not a parser performance budget.
func TestParseScalesLinearly(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range largeDocuments {
		t.Run(fixture.name, func(t *testing.T) {
			// A wider sibling range makes repeated media-default scans visible
			// above the substantial linear cost of reading each enclosure.
			sizes := []int{256}
			if fixture.name == "media" {
				sizes = append(sizes, 1024)
			}
			for _, small := range sizes {
				t.Run(strconv.Itoa(small), func(t *testing.T) {
					measure := func(n, repeats int) (float64, float64) {
						t.Helper()
						ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
						defer cancel()
						cmd := exec.CommandContext(ctx, binary, "-test.run=^TestParseScalingWorker$", "-test.count=1", "-test.v")
						cmd.Env = append(os.Environ(), scalingEnv+"="+fmt.Sprintf("%s,%d,%d", fixture.name, n, repeats))
						output, err := cmd.CombinedOutput()
						if err != nil {
							t.Fatalf("scaling worker: %v\n%s", err, output)
						}
						var stats *scalingMeasurement
						for line := range strings.Lines(string(output)) {
							if raw, ok := strings.CutPrefix(line, scalingPrefix); ok {
								var value scalingMeasurement
								if stats != nil || json.Unmarshal([]byte(raw), &value) != nil {
									t.Fatalf("invalid scaling measurement: %s", output)
								}
								stats = &value
							}
						}
						cpu := cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()
						if stats == nil || stats.Operations != repeats || stats.Bytes == 0 || cpu <= 0 {
							t.Fatalf("missing or incomplete scaling measurement: CPU %v, %s", cpu, output)
						}
						return float64(cpu) / float64(repeats), float64(stats.Bytes) / float64(repeats)
					}
					smallCPU, smallBytes := measure(small, 32)
					largeCPU, largeBytes := measure(8*small, 4)
					cpuGrowth, bytesGrowth := largeCPU/smallCPU, largeBytes/smallBytes
					t.Logf("8x input: normalized worker CPU %.2fx (%v -> %v), allocations %.2fx (%.0f -> %.0f bytes/op)", cpuGrowth, time.Duration(smallCPU), time.Duration(largeCPU), bytesGrowth, smallBytes, largeBytes)
					if cpuGrowth > 24 || bytesGrowth > 24 {
						t.Errorf("superlinear parsing: 8x input used %.2fx CPU, %.2fx allocated bytes; limit 24x", cpuGrowth, bytesGrowth)
					}
				})
			}
		})
	}
}
