package repomap

import "math"

const (
	dampingFactor  = 0.85
	personalWeight = 0.15 // fraction of rank that goes to personalization nodes
)

// pageRank runs the PageRank algorithm on the file reference graph.
//
// nodes is the full list of file paths (graph nodes).
// edges maps src → (dst → edge weight); weights are reference counts.
// personal maps file paths to personalization weights (used to bias results
// toward files mentioned in the current conversation).  Pass nil or empty for
// a uniform distribution.
//
// Returns a map from file path to PageRank score.
func pageRank(nodes []string, edges map[string]map[string]float64, personal map[string]float64, iterations int) map[string]float64 {
	n := len(nodes)
	if n == 0 {
		return nil
	}

	// Normalise personalisation vector.
	personalSum := 0.0
	for _, v := range personal {
		personalSum += v
	}
	personNorm := make(map[string]float64, len(personal))
	if personalSum > 0 {
		for k, v := range personal {
			personNorm[k] = v / personalSum
		}
	} else {
		// Uniform distribution over all nodes.
		for _, node := range nodes {
			personNorm[node] = 1.0 / float64(n)
		}
	}

	// Pre-compute out-weights for normalised edge traversal.
	outSum := make(map[string]float64, n)
	for src, dsts := range edges {
		for _, w := range dsts {
			outSum[src] += w
		}
	}

	// Initialise scores uniformly.
	scores := make(map[string]float64, n)
	for _, node := range nodes {
		scores[node] = 1.0 / float64(n)
	}

	for iter := 0; iter < iterations; iter++ {
		next := make(map[string]float64, n)

		// Dangling mass (nodes with no outgoing edges).
		danglingMass := 0.0
		for _, node := range nodes {
			if outSum[node] == 0 {
				danglingMass += scores[node]
			}
		}

		for _, node := range nodes {
			// Personalisation / teleport term.
			next[node] += (1 - dampingFactor) * personNorm[node]
			// Dangling redistribution via personalisation.
			next[node] += dampingFactor * danglingMass * personNorm[node]
		}

		// Link-following term.
		for src, dsts := range edges {
			if outSum[src] == 0 {
				continue
			}
			for dst, w := range dsts {
				next[dst] += dampingFactor * scores[src] * (w / outSum[src])
			}
		}

		// Check convergence.
		delta := 0.0
		for _, node := range nodes {
			delta += math.Abs(next[node] - scores[node])
		}
		scores = next
		if delta < 1e-6 {
			break
		}
	}

	return scores
}
