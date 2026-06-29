package ec2

import "testing"

// sampleIopsDoc mirrors the real AWS instance-type docs: a markdown body with an
// "Instance store specifications" table embedded as HTML whose IOPS column is
// "100% random read IOPS / Write IOPS". It includes a family-header colspan row,
// an HDD row with an empty IOPS cell, and a leading unrelated table (which has no
// IOPS column and must be ignored).
const sampleIopsDoc = `# Storage instances

Some intro text that should be ignored.

<table>
<thead>
  <tr><th>Instance type</th><th>vCPUs</th></tr>
</thead>
<tbody>
  <tr><td>c7gd.medium</td><td>1</td></tr>
</tbody>
</table>

More prose.

<table>
<thead>
  <tr><th>Instance type</th><th>Instance store volumes</th><th>Instance store type</th><th>100% random read IOPS / Write IOPS</th><th>TRIM support</th></tr>
</thead>
<tbody>
  <tr><td colspan="5">C7gd</td></tr>
  <tr><td>c7gd.medium</td><td>1 x 59 GB</td><td>NVMe SSD</td><td>16,250 / 13,750</td><td>&#10003; Yes</td></tr>
  <tr><td>c7gd.large</td><td>1 x 118 GB</td><td>NVMe SSD</td><td>33,542 / 27,917</td><td>&#10003; Yes</td></tr>
  <tr><td colspan="5">D2</td></tr>
  <tr><td>d2.xlarge</td><td>3 x 2048 GB</td><td>HDD</td><td></td><td> </td></tr>
</tbody>
</table>

Trailing prose ends the table.
`

func TestParseIopsTable(t *testing.T) {
	got := parseIopsTable(sampleIopsDoc)

	want := map[string]storageIops{
		"c7gd.medium": {readIops: 16250, writeIops: 13750},
		"c7gd.large":  {readIops: 33542, writeIops: 27917},
	}

	if len(got) != len(want) {
		t.Fatalf("parsed %d entries, want %d: %+v", len(got), len(want), got)
	}
	for instanceType, wantIops := range want {
		gotIops, ok := got[instanceType]
		if !ok {
			t.Errorf("missing entry for %s", instanceType)
			continue
		}
		if gotIops != wantIops {
			t.Errorf("IOPS for %s = %+v, want %+v", instanceType, gotIops, wantIops)
		}
	}

	// Family-header colspan rows and HDD rows without IOPS data must be skipped
	// rather than producing fabricated entries.
	for _, skipped := range []string{"c7gd", "d2.xlarge"} {
		if _, ok := got[skipped]; ok {
			t.Errorf("expected %q to be skipped, but it was parsed", skipped)
		}
	}
}

func TestParseIopsTableNoTable(t *testing.T) {
	// A doc with no IOPS table must yield an empty map, not a panic.
	if got := parseIopsTable("# No table here\n\nJust prose.\n"); len(got) != 0 {
		t.Errorf("expected no entries for a doc without an IOPS table, got %+v", got)
	}
}
