package settings

import (
	"strconv"

	"charm.land/huh/v2"
)

type snapshotsBuffers struct {
	retentionDays string
	maxFileBytes  string
}

func newSnapshotsPane(s *state) sectionPane {
	b := &snapshotsBuffers{}
	return newScalarPane(func() *huh.Form {
		b.retentionDays = strconv.Itoa(s.cfg.Snapshots.RetentionDays)
		b.maxFileBytes = strconv.Itoa(s.cfg.Snapshots.MaxFileBytes)
		return newSectionForm(
			huh.NewConfirm().Key("Enabled").Title("Enabled").
				Description("capture before-write snapshots of changed files").Value(&s.cfg.Snapshots.Enabled),
			numField("Retention days", &b.retentionDays, 0, func(v int) { s.cfg.Snapshots.RetentionDays = v }),
			numField("Max file bytes", &b.maxFileBytes, 0, func(v int) { s.cfg.Snapshots.MaxFileBytes = v }),
		)
	})
}
