package config

import (
	"testing"

	"marshal/internal/imagepayload"
)

// specImages is the §10 default block the [images] section ships with.
func specImages() ImagesConfig {
	return ImagesConfig{
		MaxInputBytes:   20971520,
		MaxStoredBytes:  4194304,
		MaxEdge:         1568,
		JPEGQuality:     85,
		MaxPerMessage:   10,
		MaxRequestBytes: 12582912,
		MaxStoreBytes:   536870912,
		RetainDays:      14,
	}
}

func TestImagesDefaults(t *testing.T) {
	if got := Default().Images; got != specImages() {
		t.Fatalf("Default().Images = %+v, want %+v", got, specImages())
	}
}

// TestImagesNormalizeReplacesNonPositive pins that a zero (the value a
// file-supplied table leaves behind for keys it does not name) and a
// nonsense negative both degrade to the default rather than disabling a
// limit.
func TestImagesNormalizeReplacesNonPositive(t *testing.T) {
	zero := ImagesConfig{}
	if got := zero.Normalize(); got != specImages() {
		t.Fatalf("zero ImagesConfig normalized to %+v, want %+v", got, specImages())
	}

	negative := ImagesConfig{
		MaxInputBytes:   -1,
		MaxStoredBytes:  -1,
		MaxEdge:         -1,
		JPEGQuality:     -1,
		MaxPerMessage:   -1,
		MaxRequestBytes: -1,
		MaxStoreBytes:   -1,
		RetainDays:      -1,
	}
	if got := negative.Normalize(); got != specImages() {
		t.Fatalf("negative ImagesConfig normalized to %+v, want %+v", got, specImages())
	}

	// An explicitly set value survives normalization untouched.
	set := specImages()
	set.MaxEdge = 800
	if got := set.Normalize(); got != set {
		t.Fatalf("configured ImagesConfig normalized to %+v, want %+v", got, set)
	}
}

// TestImagesMergePerKey pins that a file naming one key keeps the default
// for every other key — the whole reason configFile.Images is a pointer
// mirror rather than a plain struct.
func TestImagesMergePerKey(t *testing.T) {
	edge := 800
	cfg := Default()
	if err := merge(&cfg, configFile{Images: &fileImages{MaxEdge: &edge}}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	want := specImages()
	want.MaxEdge = 800
	if cfg.Images != want {
		t.Fatalf("Images = %+v, want %+v", cfg.Images, want)
	}
}

// TestImagesProjectLayerKeepsUserValue pins the layering consequence: a
// project [images] table naming only retain_days must not reset the user
// layer's max_edge to the default.
func TestImagesProjectLayerKeepsUserValue(t *testing.T) {
	edge, days := 800, 3
	cfg := Default()
	if err := merge(&cfg, configFile{Images: &fileImages{MaxEdge: &edge}}); err != nil {
		t.Fatalf("merge user layer: %v", err)
	}
	if err := merge(&cfg, configFile{Images: &fileImages{RetainDays: &days}}); err != nil {
		t.Fatalf("merge project layer: %v", err)
	}
	if cfg.Images.MaxEdge != 800 || cfg.Images.RetainDays != 3 {
		t.Fatalf("Images = %+v, want MaxEdge 800 and RetainDays 3", cfg.Images)
	}
}

// TestImagesPayloadConfigMatches pins that the bridge to the store's own
// config type carries all eight numbers, in step.
func TestImagesPayloadConfigMatches(t *testing.T) {
	c := ImagesConfig{
		MaxInputBytes:   1000001,
		MaxStoredBytes:  2000002,
		MaxEdge:         601,
		JPEGQuality:     61,
		MaxPerMessage:   7,
		MaxRequestBytes: 3000003,
		MaxStoreBytes:   4000004,
		RetainDays:      7,
	}
	want := imagepayload.Config{
		MaxInputBytes:   1000001,
		MaxStoredBytes:  2000002,
		MaxEdge:         601,
		JPEGQuality:     61,
		MaxPerMessage:   7,
		MaxRequestBytes: 3000003,
		MaxStoreBytes:   4000004,
		RetainDays:      7,
	}
	if got := c.PayloadConfig(); got != want {
		t.Fatalf("PayloadConfig() = %+v, want %+v", got, want)
	}
}
