package lib

import (
	"fmt"
	"strings"
)

const BrailleBase = 0x2800

var BrailleBits = [4][2]uint{
	{0, 3},
	{1, 4},
	{2, 5},
	{6, 7},
}

type yAxis struct {
	enabled     bool
	label       map[int]string
	maxLabelLen int
}

type yRange struct {
	minV float64
	maxV float64
}

type BrailleGraph struct {
	width     int
	height    int
	pw        int
	ph        int
	yAxis     yAxis
	yRange    yRange
	pixelData [][]bool
	graph     strings.Builder
}

func NewBrailleGraph(w, h int, yMin, yMax float64) *BrailleGraph {
	pw := 2 * w
	ph := 4 * h
	pd := make([][]bool, ph)
	for i := range ph {
		pd[i] = make([]bool, pw)
	}
	return &BrailleGraph{
		width:     w,
		height:    h,
		pw:        pw,
		ph:        ph,
		yAxis:     yAxis{enabled: false},
		yRange:    yRange{minV: yMin, maxV: yMax},
		pixelData: pd,
	}
}

func (bg *BrailleGraph) Plot(data []float64) string {
	bg.clear()

	for i := range bg.pw {
		fxIdx := float64(i) * float64(len(data)-1) / float64(bg.pw-1)
		xIdx := int(fxIdx)

		var v float64
		if xIdx == len(data)-1 {
			v = data[len(data)-1]
		} else {
			fraction := fxIdx - float64(xIdx)
			v = data[xIdx]*(1-fraction) + data[xIdx+1]*fraction
		}

		minV, maxV := bg.yRange.minV, bg.yRange.maxV
		v = min(maxV, v)
		yIdx := int((maxV - v) / (maxV - minV) * float64(bg.ph-1))
		for j := yIdx; j < bg.ph; j++ {
			bg.pixelData[j][i] = true
		}
	}

	for i := range bg.height {

		bg.plotYAxis(i)

		for j := range bg.width {

			mask := 0
			for pi := range 4 {
				for pj := range 2 {
					if bg.pixelData[i*4+pi][j*2+pj] {
						mask |= 1 << BrailleBits[pi][pj]
					}
				}
			}
			bg.graph.WriteRune(rune(BrailleBase + mask))
		}

		if i != bg.height-1 {
			bg.graph.WriteByte('\n')
		}
	}

	return bg.graph.String()
}

func (bg *BrailleGraph) plotYAxis(i int) {
	if bg.yAxis.enabled {
		if len(bg.yAxis.label) != 0 {
			fmt.Fprintf(&bg.graph, "%*s|", bg.yAxis.maxLabelLen, bg.yAxis.label[i])
		} else {
			bg.graph.WriteByte('|')
		}
	}
}

func (bg *BrailleGraph) SetYAxis() {
	bg.yAxis = yAxis{
		enabled: true,
		label:   make(map[int]string),
	}
}

func (bg *BrailleGraph) SetYLabel(k float64, v string) {
	if !bg.yAxis.enabled {
		return
	}
	if k >= bg.yRange.minV && k <= bg.yRange.maxV {
		idx := int((bg.yRange.maxV - k) / (bg.yRange.maxV - bg.yRange.minV) * float64(bg.height-1))
		bg.yAxis.label[idx] = v
	}
	bg.yAxis.maxLabelLen = max(bg.yAxis.maxLabelLen, len(v))
}

func (bg *BrailleGraph) clear() {
	for i := range bg.ph {
		for j := range bg.pw {
			bg.pixelData[i][j] = false
		}
	}
	bg.graph.Reset()
}
