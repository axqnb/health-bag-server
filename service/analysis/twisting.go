package analysis

import (
	"context"
	"fmt"
	"math"
	"math/cmplx"
	"sort"

	"gonum.org/v1/gonum/dsp/fourier"
	"health-bag-server/config"
	"health-bag-server/model"
)

type oscillationWindow struct{ start, end, frequency, amplitude float64 }

func twisting(ctx context.Context, samples []model.OdomSample, cfg config.Twisting) (model.Twisting, error) {
	result := model.Twisting{Status: "missing", SampleCount: int64(len(samples))}
	if len(samples) == 0 {
		return result, nil
	}
	values := make([]float64, len(samples))
	for i, sample := range samples {
		values[i] = sample.Angular
	}
	result.AngularVelocity = Stats(values)
	result.Status = "insufficient"
	points := append([]model.OdomSample{}, samples...)
	sort.SliceStable(points, func(i, j int) bool { return points[i].SourceNS < points[j].SourceNS })
	dedup := points[:0]
	for _, point := range points {
		if len(dedup) > 0 && dedup[len(dedup)-1].SourceNS == point.SourceNS {
			dedup[len(dedup)-1] = point
		} else {
			dedup = append(dedup, point)
		}
	}
	points = dedup
	var gaps []float64
	for i := 1; i < len(points); i++ {
		gap := float64(points[i].SourceNS-points[i-1].SourceNS) / 1e9
		if gap > 0 && gap <= cfg.MaxGapSeconds {
			gaps = append(gaps, gap)
		}
	}
	if len(gaps) == 0 {
		return result, nil
	}
	sort.Float64s(gaps)
	dt := gaps[len(gaps)/2]
	if dt < 0.0001 {
		return result, fmt.Errorf("里程计采样时间间隔过小，无法进行频谱分析")
	}
	maxFrequency := math.Min(cfg.MaxFrequency, 0.5/dt)
	if cfg.MinFrequency >= maxFrequency {
		return result, nil
	}
	windowSize := int(math.Round(cfg.WindowSeconds / dt))
	if windowSize < 8 {
		windowSize = 8
	}
	if windowSize > 5000000 {
		return result, fmt.Errorf("频谱分析窗口超过支持范围")
	}
	fft := fourier.NewFFT(windowSize)
	var windows []oscillationWindow
	for begin := 0; begin < len(points); {
		end := begin + 1
		for end < len(points) && float64(points[end].SourceNS-points[end-1].SourceNS)/1e9 <= cfg.MaxGapSeconds {
			end++
		}
		segment := points[begin:end]
		begin = end
		if len(segment) < 2 {
			continue
		}
		duration := float64(segment[len(segment)-1].SourceNS-segment[0].SourceNS) / 1e9
		count := int(math.Floor(duration/dt)) + 1
		if count < windowSize {
			continue
		}
		if count > 5000000 {
			return result, fmt.Errorf("重采样后的里程计数据量超过支持范围")
		}
		uniform := make([]float64, count)
		cursor := 1
		for i := range uniform {
			t := segment[0].SourceNS + int64(math.Round(float64(i)*dt*1e9))
			for cursor < len(segment)-1 && segment[cursor].SourceNS < t {
				cursor++
			}
			a, b := segment[cursor-1], segment[cursor]
			ratio := float64(t-a.SourceNS) / float64(b.SourceNS-a.SourceNS)
			ratio = math.Max(0, math.Min(1, ratio))
			uniform[i] = a.Angular + (b.Angular-a.Angular)*ratio
		}
		step := windowSize / 2
		for offset := 0; offset+windowSize <= len(uniform); {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			frequency, amplitude := spectrum(fft, uniform[offset:offset+windowSize], dt, cfg.MinFrequency, maxFrequency)
			start := float64(segment[0].SourceNS)/1e9 + float64(offset)*dt
			windows = append(windows, oscillationWindow{start, start + float64(windowSize)*dt, frequency, amplitude})
			if offset+windowSize == len(uniform) {
				break
			}
			offset += step
			if offset+windowSize > len(uniform) {
				offset = len(uniform) - windowSize
			}
		}
	}
	if len(windows) == 0 {
		return result, nil
	}
	peak := windows[0]
	for _, window := range windows {
		if window.amplitude > peak.amplitude {
			peak = window
		}
	}
	result.DominantFreq = model.Float(peak.frequency)
	result.MainFreqDominant = model.Float(peak.amplitude)
	result.Status = "features_only"
	if cfg.AmplitudeThreshold == nil {
		return result, nil
	}
	var intervals []oscillationWindow
	for _, window := range windows {
		if window.amplitude < *cfg.AmplitudeThreshold {
			continue
		}
		if len(intervals) > 0 && window.start <= intervals[len(intervals)-1].end+dt {
			intervals[len(intervals)-1].end = math.Max(intervals[len(intervals)-1].end, window.end)
		} else {
			intervals = append(intervals, window)
		}
	}
	var episodes int64
	var total, maximum float64
	for _, interval := range intervals {
		duration := interval.end - interval.start
		if duration >= cfg.MinDuration {
			episodes++
			total += duration
			maximum = math.Max(maximum, duration)
		}
	}
	result.Status = "ok"
	result.HasTwisting = model.Bool(episodes > 0)
	result.EpisodeCount = &episodes
	result.TwistingDurationTotal = &total
	result.TwistingDurationMax = &maximum
	return result, nil
}

// Amplitude is corrected for Hann gain and has units rad/s, not PSD units.
func spectrum(fft *fourier.FFT, input []float64, dt, minFrequency, maxFrequency float64) (float64, float64) {
	n := len(input)
	middle := float64(n-1) / 2
	var mean, numerator, denominator float64
	for _, value := range input {
		mean += value
	}
	mean /= float64(n)
	for i, value := range input {
		x := float64(i) - middle
		numerator += x * (value - mean)
		denominator += x * x
	}
	slope := numerator / denominator
	windowed := make([]float64, n)
	var gain float64
	for i, value := range input {
		w := 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(n-1)))
		windowed[i] = (value - mean - slope*(float64(i)-middle)) * w
		gain += w
	}
	coefficients := fft.Coefficients(nil, windowed)
	var peakFrequency, peakAmplitude float64
	for k := 1; k < len(coefficients); k++ {
		frequency := float64(k) / (float64(n) * dt)
		if frequency < minFrequency || frequency > maxFrequency {
			continue
		}
		factor := 2.0
		if n%2 == 0 && k == n/2 {
			factor = 1
		}
		amplitude := factor * cmplx.Abs(coefficients[k]) / gain
		if amplitude > peakAmplitude {
			peakAmplitude = amplitude
			peakFrequency = frequency
		}
	}
	return peakFrequency, peakAmplitude
}
