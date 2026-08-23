package detector

import (
	"math"
	"math/rand"
)

// IsolationForest implements the Liu et al. 2008 anomaly detection algorithm.
// Anomalies are isolated in fewer random splits, producing shorter path lengths.
type IsolationForest struct {
	Trees         []*iTree
	SubsampleSize int
	MaxDepth      int
}

type iTree struct {
	Root *iNode
}

type iNode struct {
	// Internal node fields
	SplitFeature int
	SplitValue   float64
	Left         *iNode
	Right        *iNode

	// Leaf node field
	Size int // number of samples that reached this leaf
}

func (n *iNode) isLeaf() bool {
	return n.Left == nil && n.Right == nil
}

// NewIsolationForest creates an untrained forest with the given parameters.
func NewIsolationForest(numTrees, subsampleSize int) *IsolationForest {
	maxDepth := int(math.Ceil(math.Log2(float64(subsampleSize))))
	return &IsolationForest{
		Trees:         make([]*iTree, numTrees),
		SubsampleSize: subsampleSize,
		MaxDepth:      maxDepth,
	}
}

// Train builds the forest from training data (normal transactions only).
func (f *IsolationForest) Train(dataset [][]float64, seed int64) {
	rng := rand.New(rand.NewSource(seed))

	for i := range f.Trees {
		// Subsample
		subsample := subsample(rng, dataset, f.SubsampleSize)
		// Build tree
		f.Trees[i] = &iTree{
			Root: buildITree(rng, subsample, 0, f.MaxDepth),
		}
	}
}

// Score returns the anomaly score for a single sample.
// Score ∈ [0, 1], higher = more anomalous.
func (f *IsolationForest) Score(sample []float64) float64 {
	if len(f.Trees) == 0 {
		return 0.5
	}

	totalPathLength := 0.0
	for _, tree := range f.Trees {
		totalPathLength += pathLength(tree.Root, sample, 0)
	}
	avgPathLength := totalPathLength / float64(len(f.Trees))

	// Normalize by expected path length c(n)
	cn := expectedPathLength(float64(f.SubsampleSize))
	if cn == 0 {
		return 0.5
	}

	// Anomaly score: s = 2^(-avgPathLength / c(n))
	score := math.Pow(2.0, -avgPathLength/cn)
	return score
}

// buildITree recursively constructs an isolation tree.
func buildITree(rng *rand.Rand, data [][]float64, depth, maxDepth int) *iNode {
	n := len(data)

	// Stop: too few samples or max depth reached
	if n <= 1 || depth >= maxDepth {
		return &iNode{Size: n}
	}

	numFeatures := len(data[0])

	// Pick a random feature
	feature := rng.Intn(numFeatures)

	// Find min/max for this feature
	minVal := data[0][feature]
	maxVal := data[0][feature]
	for _, sample := range data[1:] {
		if sample[feature] < minVal {
			minVal = sample[feature]
		}
		if sample[feature] > maxVal {
			maxVal = sample[feature]
		}
	}

	// If all values are the same, can't split
	if minVal == maxVal {
		return &iNode{Size: n}
	}

	// Pick a random split value between min and max
	splitVal := minVal + rng.Float64()*(maxVal-minVal)

	// Partition
	var left, right [][]float64
	for _, sample := range data {
		if sample[feature] < splitVal {
			left = append(left, sample)
		} else {
			right = append(right, sample)
		}
	}

	// Edge case: everything went to one side
	if len(left) == 0 || len(right) == 0 {
		return &iNode{Size: n}
	}

	return &iNode{
		SplitFeature: feature,
		SplitValue:   splitVal,
		Left:         buildITree(rng, left, depth+1, maxDepth),
		Right:        buildITree(rng, right, depth+1, maxDepth),
	}
}

// pathLength traverses the tree and returns the path length for a sample.
func pathLength(node *iNode, sample []float64, depth int) float64 {
	if node.isLeaf() {
		// At leaf: add expected path length for remaining samples
		return float64(depth) + expectedPathLength(float64(node.Size))
	}

	if sample[node.SplitFeature] < node.SplitValue {
		return pathLength(node.Left, sample, depth+1)
	}
	return pathLength(node.Right, sample, depth+1)
}

// expectedPathLength computes c(n), the average path length of unsuccessful search
// in a Binary Search Tree. This is the normalization constant from the paper.
// c(n) = 2*H(n-1) - 2*(n-1)/n, where H(i) is the harmonic number.
func expectedPathLength(n float64) float64 {
	if n <= 1 {
		return 0
	}
	if n == 2 {
		return 1
	}
	// H(n-1) ≈ ln(n-1) + Euler-Mascheroni constant (0.5772156649)
	h := math.Log(n-1) + 0.5772156649
	return 2.0*h - 2.0*(n-1.0)/n
}

// subsample randomly selects up to k samples from dataset.
func subsample(rng *rand.Rand, dataset [][]float64, k int) [][]float64 {
	n := len(dataset)
	if n <= k {
		result := make([][]float64, n)
		copy(result, dataset)
		return result
	}

	// Fisher-Yates partial shuffle
	indices := make([]int, n)
	for i := range indices {
		indices[i] = i
	}
	for i := 0; i < k; i++ {
		j := i + rng.Intn(n-i)
		indices[i], indices[j] = indices[j], indices[i]
	}

	result := make([][]float64, k)
	for i := 0; i < k; i++ {
		result[i] = dataset[indices[i]]
	}
	return result
}