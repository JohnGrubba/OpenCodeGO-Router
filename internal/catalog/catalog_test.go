package catalog

import "testing"

func TestDeepSeekPeakMetadata(t *testing.T) {
	for _, id := range []string{"deepseek-v4.1-flash", "deepseek-v4-pro"} {
		m, ok := Get(id)
		if !ok {
			t.Fatalf("missing %s", id)
		}
		if m.Peak == nil || m.Peak.Multiplier != 2 {
			t.Fatalf("%s peak metadata = %+v", id, m.Peak)
		}
		if len(m.Peak.Windows) != 2 || m.Peak.Days != "Mon-Fri" || m.Peak.Timezone != "UTC" {
			t.Fatalf("%s peak windows = %+v", id, m.Peak)
		}
	}
	if m, _ := Get("kimi-k3"); m.Peak != nil {
		t.Fatalf("kimi should not have peak metadata")
	}
}
