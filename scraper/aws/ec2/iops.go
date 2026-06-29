package ec2

import (
	"fmt"
	"log"
	"regexp"
	"scraper/utils"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// AWS publishes per-family instance-type docs whose body is markdown but whose
// "Instance store specifications" table is embedded as an HTML <table>. Each such
// table has a "100% random read IOPS / Write IOPS" column keyed by instance type.
// We scrape those tables to expose the instance-store random read/write IOPS on
// each instance.
const awsInstanceTypeDocBase = "https://docs.aws.amazon.com/ec2/latest/instancetypes"

// iopsDocSlugs are the instance-type doc families that carry instance-store
// specs (general purpose, compute, memory, storage, accelerated, HPC).
var iopsDocSlugs = []string{"gp", "co", "mo", "so", "ac", "hpc"}

var (
	iopsHeaderRe     = regexp.MustCompile(`(?i)random read IOPS`)
	instanceHeaderRe = regexp.MustCompile(`(?i)instance type`)
	// instanceTypeRe matches a "." followed by a word char, the same heuristic
	// the original Node scraper used to tell instance-type rows (e.g. "m5.large")
	// apart from family-header rows (e.g. a "D2" colspan row).
	instanceTypeRe = regexp.MustCompile(`\.\w`)
)

type storageIops struct {
	readIops  int
	writeIops int
}

// parseIopsValue mimics JS parseInt after stripping thousands separators: it
// reads the leading (optionally signed) integer prefix and ignores any trailing
// text. Returns ok=false when there is no leading integer.
func parseIopsValue(s string) (int, bool) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	end := 0
	if end < len(s) && (s[end] == '+' || s[end] == '-') {
		end++
	}
	start := end
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == start {
		return 0, false
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

// findNodes returns every descendant element of n with the given tag name.
func findNodes(n *html.Node, tag string) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == tag {
			out = append(out, node)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

// nodeText returns the concatenated, trimmed text content of a node.
func nodeText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(sb.String())
}

// rowCells returns the trimmed text of a row's direct <td>/<th> cells, and
// whether the row is a header row (contains any <th>).
func rowCells(tr *html.Node) (cells []string, isHeader bool) {
	for c := tr.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || (c.Data != "td" && c.Data != "th") {
			continue
		}
		cells = append(cells, nodeText(c))
		if c.Data == "th" {
			isHeader = true
		}
	}
	return cells, isHeader
}

// parseIopsTable parses the instance-store IOPS table out of one AWS
// instance-type doc, returning a lowercase-instance-type -> IOPS map. It scans
// every HTML <table>, locates the one whose header row carries the "random read
// IOPS" column, then reads each data row, skipping family-header rows and rows
// without IOPS data.
func parseIopsTable(content string) map[string]storageIops {
	results := map[string]storageIops{}

	doc, err := html.Parse(strings.NewReader(content))
	if err != nil {
		return results
	}

	for _, table := range findNodes(doc, "table") {
		iopsColIdx, instanceColIdx := -1, -1

		for _, row := range findNodes(table, "tr") {
			cells, isHeader := rowCells(row)

			if isHeader {
				// Re-locate the columns from this header. A table without a
				// "random read IOPS" header leaves iopsColIdx at -1, so its data
				// rows are skipped below.
				iopsColIdx, instanceColIdx = -1, -1
				for i, c := range cells {
					if iopsColIdx == -1 && iopsHeaderRe.MatchString(c) {
						iopsColIdx = i
					}
					if instanceColIdx == -1 && instanceHeaderRe.MatchString(c) {
						instanceColIdx = i
					}
				}
				if instanceColIdx == -1 {
					instanceColIdx = 0
				}
				continue
			}

			if iopsColIdx == -1 {
				continue
			}
			if instanceColIdx >= len(cells) || iopsColIdx >= len(cells) {
				continue
			}

			// Skip family-header rows (e.g. a single "D2" colspan cell) and any
			// non-instance-type row.
			instanceType := cells[instanceColIdx]
			if instanceType == "" || !instanceTypeRe.MatchString(instanceType) {
				continue
			}

			iopsCell := cells[iopsColIdx]
			if iopsCell == "" {
				continue
			}
			parts := strings.Split(iopsCell, "/")
			if len(parts) != 2 {
				continue
			}
			readIops, okRead := parseIopsValue(parts[0])
			writeIops, okWrite := parseIopsValue(parts[1])
			if !okRead || !okWrite {
				continue
			}

			results[strings.ToLower(instanceType)] = storageIops{
				readIops:  readIops,
				writeIops: writeIops,
			}
		}
	}

	return results
}

// addStorageIopsInfo fetches the AWS instance-type docs, parses the
// instance-store random read/write IOPS tables, and sets the values on each
// matching instance that has instance storage. Instances without a documented
// value are left untouched (no fabricated zeros). Fails loud on fetch errors or
// when nothing parses/matches, so a broken scrape never ships empty data.
func addStorageIopsInfo(instances map[string]*EC2Instance) {
	log.Default().Println("Adding instance store IOPS to EC2")

	allIops := map[string]storageIops{}
	for _, slug := range iopsDocSlugs {
		url := fmt.Sprintf("%s/%s.md", awsInstanceTypeDocBase, slug)
		body, err := utils.FetchWithRetry(url, nil)
		if err != nil {
			log.Fatalln("Failed to fetch instance store IOPS doc", url, err)
		}
		parsed := parseIopsTable(string(body))
		log.Default().Printf("%s.md: %d instance types with IOPS", slug, len(parsed))
		for k, v := range parsed {
			allIops[k] = v
		}
	}

	if len(allIops) == 0 {
		log.Fatalln("Parsed zero instance store IOPS entries across all docs")
	}

	matched := 0
	for instanceType, instance := range instances {
		iops, ok := allIops[strings.ToLower(instanceType)]
		if !ok {
			continue
		}
		if instance.Storage == nil {
			continue
		}
		readIops := iops.readIops
		writeIops := iops.writeIops
		instance.Storage.StorageReadIops = &readIops
		instance.Storage.StorageWriteIops = &writeIops
		matched++
	}

	if matched == 0 {
		log.Fatalln("Zero instances matched instance store IOPS data; IOPS entries:", len(allIops))
	}

	log.Default().Printf("Merged instance store IOPS into %d instances (of %d IOPS entries)", matched, len(allIops))
}
