package schema

type EmbedRequest struct {
	Model string
	Input []string
}

type EmbedResponse struct {
	Model      string
	Embeddings [][]float64
}
