package aws

import (
	"math"
	"testing"

	"scraper/aws/awsutils"
)

func TestSqlServerVcpuShouldBeHalved(t *testing.T) {
	cases := map[string]bool{
		// 7th-gen-and-above Intel families at 2xlarge and above: halved.
		"db.m7i.2xlarge":  true,
		"db.m7i.4xlarge":  true,
		"db.m7i.48xlarge": true,
		"db.m8i.2xlarge":  true,
		"db.m8i.96xlarge": true,
		"db.r7i.2xlarge":  true,
		"db.r8i.12xlarge": true,

		// Same families below 2xlarge: unchanged.
		"db.m7i.large":  false,
		"db.m7i.xlarge": false,
		"db.r7i.large":  false,

		// Older / non-Intel-7th-gen families at 2xlarge: unchanged.
		"db.m6i.2xlarge": false,
		"db.r6i.2xlarge": false,
		"db.m5.2xlarge":  false,
		"db.t3.2xlarge":  false,
		"db.m7g.2xlarge": false, // Graviton, not Intel
		"db.c7i.2xlarge": false, // not offered on RDS SQL Server

		// The "db." prefix is optional; family/size still parse correctly.
		"m7i.2xlarge": true,

		// Malformed / edge inputs: unchanged.
		"db.m7i":        false,
		"db.m7i.medium": false,
	}

	for instanceType, want := range cases {
		got := sqlServerVcpuShouldBeHalved(instanceType)
		if got != want {
			t.Errorf("sqlServerVcpuShouldBeHalved(%q) = %v, want %v", instanceType, got, want)
		}
	}
}

// newVcpuInstance builds a minimal instance map shaped like the scraper produces,
// with vcpu stored as an Averager of strings.
func newVcpuInstance(instanceType string, vcpus ...string) map[string]any {
	avg := &awsutils.Averager[string]{}
	*avg = append(*avg, vcpus...)
	return map[string]any{
		"instance_type": instanceType,
		"vcpu":          avg,
	}
}

func vcpuValue(t *testing.T, instance map[string]any) string {
	t.Helper()
	avg, ok := instance["vcpu"].(*awsutils.Averager[string])
	if !ok {
		t.Fatalf("vcpu is not an *Averager: %T", instance["vcpu"])
	}
	return avg.Value()
}

func TestHalveSqlServerVcpu(t *testing.T) {
	// The real failing scenario from issue #899: db.m7i.2xlarge currently shows
	// the EC2 vCPU count (8) but RDS SQL Server only exposes 4.
	instance := newVcpuInstance("db.m7i.2xlarge", "8")
	halveSqlServerVcpu(instance)
	if got := vcpuValue(t, instance); got != "4" {
		t.Errorf("db.m7i.2xlarge SQL Server vcpu = %q, want 4", got)
	}

	// A larger affected size halves too.
	instance = newVcpuInstance("db.r8i.12xlarge", "48")
	halveSqlServerVcpu(instance)
	if got := vcpuValue(t, instance); got != "24" {
		t.Errorf("db.r8i.12xlarge SQL Server vcpu = %q, want 24", got)
	}

	// Below 2xlarge on the same family stays unchanged.
	instance = newVcpuInstance("db.m7i.xlarge", "4")
	halveSqlServerVcpu(instance)
	if got := vcpuValue(t, instance); got != "4" {
		t.Errorf("db.m7i.xlarge vcpu = %q, want 4 (unchanged)", got)
	}

	// A non-7th-gen family at 2xlarge stays unchanged.
	instance = newVcpuInstance("db.m6i.2xlarge", "8")
	halveSqlServerVcpu(instance)
	if got := vcpuValue(t, instance); got != "8" {
		t.Errorf("db.m6i.2xlarge vcpu = %q, want 8 (unchanged)", got)
	}

	// A Graviton family at 2xlarge stays unchanged.
	instance = newVcpuInstance("db.m7g.2xlarge", "8")
	halveSqlServerVcpu(instance)
	if got := vcpuValue(t, instance); got != "8" {
		t.Errorf("db.m7g.2xlarge vcpu = %q, want 8 (unchanged)", got)
	}
}

// assembleOnDemand runs the real on-demand price-assembly path for a single SQL
// Server SKU and returns the On-Demand price stored for its engine code (which is
// what the frontend "SQL Server <edition> On Demand Cost" column reads).
func assembleOnDemand(
	attributes map[string]string,
	basePrice string,
	unbundled map[string]bool,
	licenseRates map[engineCode]float64,
) float64 {
	instance := map[string]any{
		"pricing": make(map[string]map[string]any),
	}
	getPricing := func(platform string) *genericAwsPricingData {
		return getgenericAwsPricingData(instance, "us-east-1", platform)
	}
	pd := awsutils.RegionPriceDimension{
		Description:  "instance hour running SQL Server",
		PricePerUnit: map[string]string{"USD": basePrice},
	}
	processRdsOnDemandDimension(
		attributes,
		attributes["instanceType"],
		pd,
		getPricing,
		"USD",
		unbundled,
		licenseRates,
	)
	return getPricing(attributes["engineCode"]).OnDemand
}

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// TestUnbundledSqlServerLicense replicates the #890 scenario: a db.m7i.2xlarge SQL
// Server Standard "unbundled" instance must display base + separately-billed
// Windows + SQL Server license cost.
//
// Base $0.824/hr (the launch price from #890), 4 vCPU (as AWS reports this type in
// the AmazonRDS offer), license rates per vCPU-hour from the AmazonRDSOCPULicenseFees
// offer: SQL Server Standard $0.120 + Windows OS $0.046 = $0.166, summed in the
// license map keyed by engine code "12". All-in = 0.824 + 4*0.166 = $1.488/hr.
//
// The load-bearing, AWS-verified quantity is the $0.664/hr surcharge (4 * $0.166);
// AWS has since lowered the us-east-1 base to $0.712, but the surcharge logic this
// test exercises is independent of the base price.
func TestUnbundledSqlServerLicense(t *testing.T) {
	// Summed SQL Server + Windows license rate per vCPU-hour, keyed by engine code,
	// as GetRdsLicenseFees produces it from the AmazonRDSOCPULicenseFees offer.
	licenseRates := map[engineCode]float64{
		"11": 0.017 + 0.046, // Web
		"12": 0.120 + 0.046, // Standard
		"15": 0.375 + 0.046, // Enterprise
	}

	unbundled := map[string]bool{
		"db.m7i.2xlarge": true,
	}

	stdAttrs := map[string]string{
		"instanceType":    "db.m7i.2xlarge",
		"databaseEngine":  "SQL Server",
		"databaseEdition": "Standard",
		"licenseModel":    "License included",
		"engineCode":      "12",
		"vcpu":            "4",
	}

	got := assembleOnDemand(stdAttrs, "0.824", unbundled, licenseRates)
	if !approxEqual(got, 1.488) {
		t.Errorf("unbundled db.m7i.2xlarge SQL Server Standard On Demand = %v, want 1.488", got)
	}

	// A bundled / older family (db.m5.2xlarge) is NOT in the unbundled set; its
	// "License included" base price already bakes in the license and must pass
	// through unchanged ($2.548/hr).
	bundledAttrs := map[string]string{
		"instanceType":    "db.m5.2xlarge",
		"databaseEngine":  "SQL Server",
		"databaseEdition": "Standard",
		"licenseModel":    "License included",
		"engineCode":      "12",
		"vcpu":            "8",
	}
	gotBundled := assembleOnDemand(bundledAttrs, "2.548", map[string]bool{}, licenseRates)
	if !approxEqual(gotBundled, 2.548) {
		t.Errorf("bundled db.m5.2xlarge SQL Server Standard On Demand = %v, want 2.548 (unchanged)", gotBundled)
	}
}

// TestUnbundledSqlServerLicenseSurcharge checks the surcharge helper in isolation,
// including that non-SQL-Server and non-unbundled instances get no surcharge.
func TestUnbundledSqlServerLicenseSurcharge(t *testing.T) {
	licenseRates := map[engineCode]float64{"12": 0.166}
	unbundled := map[string]bool{"db.m7i.2xlarge": true}

	cases := []struct {
		name  string
		attrs map[string]string
		want  float64
	}{
		{
			name: "unbundled sql server standard license included",
			attrs: map[string]string{
				"databaseEngine": "SQL Server", "licenseModel": "License included", "engineCode": "12", "vcpu": "4",
			},
			want: 4 * 0.166,
		},
		{
			name: "unbundled sql server byom sku is not surcharged",
			attrs: map[string]string{
				"databaseEngine": "SQL Server", "licenseModel": "Bring your own media", "engineCode": "52", "vcpu": "4",
			},
			want: 0,
		},
		{
			name: "non-sql-server engine on unbundled type",
			attrs: map[string]string{
				"databaseEngine": "PostgreSQL", "licenseModel": "No license required", "engineCode": "14", "vcpu": "4",
			},
			want: 0,
		},
	}

	for _, c := range cases {
		got := unbundledSqlServerLicenseSurcharge(c.attrs, "db.m7i.2xlarge", unbundled, licenseRates)
		if !approxEqual(got, c.want) {
			t.Errorf("%s: surcharge = %v, want %v", c.name, got, c.want)
		}
	}

	// A SQL Server instance that is NOT unbundled gets no surcharge.
	got := unbundledSqlServerLicenseSurcharge(
		map[string]string{"databaseEngine": "SQL Server", "licenseModel": "License included", "engineCode": "12", "vcpu": "8"},
		"db.m5.2xlarge",
		unbundled,
		licenseRates,
	)
	if got != 0 {
		t.Errorf("bundled SQL Server surcharge = %v, want 0", got)
	}
}

// TestIsUnbundledSqlServerProduct verifies the unbundled-detection signal.
func TestIsUnbundledSqlServerProduct(t *testing.T) {
	cases := []struct {
		attrs map[string]string
		want  bool
	}{
		{map[string]string{"databaseEngine": "SQL Server", "licenseModel": "Bring your own media", "databaseEdition": "Standard"}, true},
		{map[string]string{"databaseEngine": "SQL Server", "licenseModel": "Bring your own media", "databaseEdition": "Enterprise"}, true},
		{map[string]string{"databaseEngine": "SQL Server", "licenseModel": "Bring your own media", "databaseEdition": "Web"}, true},
		// Free developer edition: not a license-bearing unbundled product.
		{map[string]string{"databaseEngine": "SQL Server", "licenseModel": "Bring your own media", "databaseEdition": "Enterprise Developer"}, false},
		// Bundled "License included" SKU.
		{map[string]string{"databaseEngine": "SQL Server", "licenseModel": "License included", "databaseEdition": "Standard"}, false},
		// Other engine.
		{map[string]string{"databaseEngine": "PostgreSQL", "licenseModel": "No license required", "databaseEdition": "None"}, false},
	}
	for _, c := range cases {
		if got := isUnbundledSqlServerProduct(c.attrs); got != c.want {
			t.Errorf("isUnbundledSqlServerProduct(%v) = %v, want %v", c.attrs, got, c.want)
		}
	}
}
