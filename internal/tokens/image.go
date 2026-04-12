package tokens

// ImageDetail controls the fidelity (and cost) of vision input.
type ImageDetail string

const (
	ImageDetailLow  ImageDetail = "low"
	ImageDetailHigh ImageDetail = "high"
	ImageDetailAuto ImageDetail = "auto"
)

const (
	// tileSize is the pixel side-length of each processing tile.
	tileSize = 512
	// tokensPerTile is the token cost of a single 512×512 high-detail tile.
	tokensPerTile = 170
	// baseCost is charged once for every image regardless of tile count.
	baseCost = 85
	// lowDetailCost is the flat cost for low-detail images.
	lowDetailCost = baseCost
	// maxLongSide is the maximum allowed long side before rescaling.
	maxLongSide = 2048
	// highDetailShortSide is the target short side after the second rescale.
	highDetailShortSide = 768
)

// CountImageTokens returns the token cost for an image with the given pixel
// dimensions and detail level. This ports aider/models.py::token_count_for_image.
//
// For detail="auto" the function treats the image as high-detail.
func CountImageTokens(widthPx, heightPx int, detail ImageDetail) int {
	if detail == ImageDetailLow {
		return lowDetailCost
	}

	// Step 1 – fit inside maxLongSide × maxLongSide while preserving aspect ratio.
	w, h := float64(widthPx), float64(heightPx)
	if w > maxLongSide || h > maxLongSide {
		scale := float64(maxLongSide) / max64(w, h)
		w *= scale
		h *= scale
	}

	// Step 2 – scale so the short side equals highDetailShortSide.
	shortSide := min64(w, h)
	if shortSide > highDetailShortSide {
		scale := float64(highDetailShortSide) / shortSide
		w *= scale
		h *= scale
	}

	// Step 3 – tile count (ceil division).
	tilesW := ceildiv(int(w), tileSize)
	tilesH := ceildiv(int(h), tileSize)

	return baseCost + tokensPerTile*tilesW*tilesH
}

func max64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func ceildiv(n, d int) int {
	return (n + d - 1) / d
}
