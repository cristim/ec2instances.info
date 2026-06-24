package aws

import (
	"scraper/aws/awsutils"
	"testing"
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
