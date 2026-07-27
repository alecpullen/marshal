package sidepanel

import (
	"testing"

	"marshal/internal/app/config"
)

func defaultCfg() config.SidePanelConfig {
	return config.Default().TUI.SidePanel
}

func TestGeometry(t *testing.T) {
	tests := []struct {
		name               string
		frame              int
		cfg                config.SidePanelConfig
		wantLeft, wantRail int
	}{
		{"below threshold", 80, defaultCfg(), 80, 0},
		{"one below threshold", 119, defaultCfg(), 119, 0},
		{"at threshold", 120, defaultCfg(), 90, 30},
		{"one above threshold", 121, defaultCfg(), 91, 30},
		{"mid range", 200, defaultCfg(), 150, 50},
		{"clamped at max", 300, defaultCfg(), 240, 60},
		{"clamped at max, wider", 400, defaultCfg(), 340, 60},
		{
			name:  "disabled",
			frame: 200,
			cfg: func() config.SidePanelConfig {
				c := defaultCfg()
				c.Enabled = false
				return c
			}(),
			wantLeft: 200, wantRail: 0,
		},
		{
			// width_pct=50 at 120 would leave the left column 60 cols,
			// below the 80-col floor. The rail yields, and because the
			// remainder (40) is above min_cols it renders at 40.
			name:  "floor guard shrinks rail",
			frame: 120,
			cfg: func() config.SidePanelConfig {
				c := defaultCfg()
				c.WidthPct = 50
				return c
			}(),
			wantLeft: 80, wantRail: 40,
		},
		{
			// At 100 cols the floor leaves only 20 for the rail, below
			// min_cols=30, so the rail is dropped entirely. (min_width is
			// lowered so the threshold is not what rejects it.)
			name:  "floor guard drops rail",
			frame: 100,
			cfg: func() config.SidePanelConfig {
				c := defaultCfg()
				c.MinWidth = 90
				c.WidthPct = 50
				return c
			}(),
			wantLeft: 100, wantRail: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left, rail := Geometry(tt.frame, 80, tt.cfg)
			if left != tt.wantLeft || rail != tt.wantRail {
				t.Errorf("Geometry(%d) = (%d, %d), want (%d, %d)",
					tt.frame, left, rail, tt.wantLeft, tt.wantRail)
			}
			if left+rail != tt.frame {
				t.Errorf("left+rail = %d, want %d (must tile the frame exactly)",
					left+rail, tt.frame)
			}
		})
	}
}
