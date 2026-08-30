#!/usr/bin/env python3
"""Compare bounded room WAV evidence without opening provider payloads."""

from __future__ import annotations

import argparse
import array
import json
import math
import pathlib
import statistics
import sys
import wave
from collections import Counter


FRAME_DURATION_MS = 20
SILENCE_FLOOR_DBFS = -55.0
FFT_SIZE = 512
TONAL_MIN_HZ = 80.0
TONAL_MAX_HZ = 8000.0
STABLE_TONE_FRACTION = 0.10


def dbfs(rms: float) -> float:
    if rms == 0:
        return float("-inf")
    return 20.0 * math.log10(rms / 32768.0)


def fft_peak_bin(samples: array.array, sample_rate: int) -> int | None:
    if not samples or not any(samples):
        return None

    values = [0j] * FFT_SIZE
    denominator = max(1, len(samples) - 1)
    for index, sample in enumerate(samples):
        window = 0.5 - 0.5 * math.cos(2.0 * math.pi * index / denominator)
        values[index] = complex(sample * window, 0.0)

    j = 0
    for index in range(1, FFT_SIZE):
        bit = FFT_SIZE >> 1
        while j & bit:
            j ^= bit
            bit >>= 1
        j ^= bit
        if index < j:
            values[index], values[j] = values[j], values[index]

    length = 2
    while length <= FFT_SIZE:
        angle = -2.0 * math.pi / length
        root = complex(math.cos(angle), math.sin(angle))
        half = length // 2
        for start in range(0, FFT_SIZE, length):
            factor = 1.0 + 0.0j
            for offset in range(half):
                even = values[start + offset]
                odd = factor * values[start + offset + half]
                values[start + offset] = even + odd
                values[start + offset + half] = even - odd
                factor *= root
        length *= 2

    first_bin = max(1, math.ceil(TONAL_MIN_HZ * FFT_SIZE / sample_rate))
    last_bin = min(FFT_SIZE // 2, math.floor(TONAL_MAX_HZ * FFT_SIZE / sample_rate))
    if first_bin > last_bin:
        return None
    return max(
        range(first_bin, last_bin + 1),
        key=lambda index: values[index].real**2 + values[index].imag**2,
    )


def stable_peaks(peak_bins: list[int], sample_rate: int) -> list[dict[str, float | int]]:
    if not peak_bins:
        return []
    counts = Counter(peak_bins)
    minimum_count = max(3, math.ceil(len(peak_bins) * STABLE_TONE_FRACTION))
    result = []
    for index, count in sorted(counts.items(), key=lambda item: (-item[1], item[0])):
        if count < minimum_count:
            continue
        result.append(
            {
                "frequency_hz": round(index * sample_rate / FFT_SIZE, 2),
                "frame_count": count,
                "fraction": round(count / len(peak_bins), 4),
            }
        )
    return result


def read_wav(path: pathlib.Path) -> tuple[int, array.array]:
    try:
        with wave.open(str(path), "rb") as wav:
            channels = wav.getnchannels()
            sample_width = wav.getsampwidth()
            sample_rate = wav.getframerate()
            raw = wav.readframes(wav.getnframes())
    except (OSError, wave.Error) as error:
        raise ValueError(f"read {path}: {error}") from error
    if channels != 1 or sample_width != 2 or sample_rate <= 0:
        raise ValueError(
            f"{path}: expected mono 16-bit PCM, got "
            f"channels={channels}, sample_width={sample_width}, sample_rate={sample_rate}"
        )
    samples = array.array("h")
    samples.frombytes(raw)
    if sys.byteorder != "little":
        samples.byteswap()
    if not samples:
        raise ValueError(f"{path}: WAV contains no samples")
    return sample_rate, samples


def analyze_file(path: pathlib.Path) -> dict:
    sample_rate, samples = read_wav(path)
    frame_samples = sample_rate * FRAME_DURATION_MS // 1000
    if frame_samples <= 0 or sample_rate * FRAME_DURATION_MS % 1000 != 0:
        raise ValueError(f"{path}: sample rate does not support a whole 20 ms frame")

    levels: list[float] = []
    silent_levels: list[float] = []
    peak_bins: list[int] = []
    exact_zero_frames = 0
    full_frame_count = 0
    for start in range(0, len(samples), frame_samples):
        frame = samples[start : start + frame_samples]
        if len(frame) == frame_samples:
            full_frame_count += 1
            if not any(frame):
                exact_zero_frames += 1
        rms = math.sqrt(sum(sample * sample for sample in frame) / len(frame))
        level = dbfs(rms)
        levels.append(level)
        if level <= SILENCE_FLOOR_DBFS:
            silent_levels.append(level)
            if len(frame) == frame_samples:
                peak_bin = fft_peak_bin(frame, sample_rate)
                if peak_bin is not None:
                    peak_bins.append(peak_bin)

    return {
        "path": path.name,
        "sample_rate_hz": sample_rate,
        "sample_count": len(samples),
        "duration_s": round(len(samples) / sample_rate, 3),
        "frame_duration_ms": FRAME_DURATION_MS,
        "frame_count": len(levels),
        "full_frame_count": full_frame_count,
        "exact_zero_frames": exact_zero_frames,
        "exact_zero_fraction": round(exact_zero_frames / len(levels), 6),
        "silence_floor_dbfs": SILENCE_FLOOR_DBFS,
        "silence_frame_count": len(silent_levels),
        "silence_dbfs_median": round(statistics.median(silent_levels), 2) if silent_levels else None,
        "stable_tonal_peaks": stable_peaks(peak_bins, sample_rate),
        "_silent_levels": silent_levels,
        "_peak_bins": peak_bins,
    }


def aggregate(files: list[dict]) -> dict:
    if not files:
        raise ValueError("at least one WAV is required")
    rates = {item["sample_rate_hz"] for item in files}
    if len(rates) != 1:
        raise ValueError("comparison WAVs must use the same sample rate")
    silent_levels = [level for item in files for level in item["_silent_levels"]]
    peak_bins = [peak for item in files for peak in item["_peak_bins"]]
    total_frames = sum(item["frame_count"] for item in files)
    exact_zero_frames = sum(item["exact_zero_frames"] for item in files)
    return {
        "file_count": len(files),
        "sample_rate_hz": files[0]["sample_rate_hz"],
        "sample_count": sum(item["sample_count"] for item in files),
        "duration_s": round(sum(item["duration_s"] for item in files), 3),
        "frame_duration_ms": FRAME_DURATION_MS,
        "frame_count": total_frames,
        "exact_zero_frames": exact_zero_frames,
        "exact_zero_fraction": round(exact_zero_frames / total_frames, 6),
        "silence_floor_dbfs": SILENCE_FLOOR_DBFS,
        "silence_frame_count": len(silent_levels),
        "silence_dbfs_median": round(statistics.median(silent_levels), 2) if silent_levels else None,
        "stable_tonal_peaks": stable_peaks(peak_bins, files[0]["sample_rate_hz"]),
    }


def public_file(item: dict) -> dict:
    return {key: value for key, value in item.items() if not key.startswith("_")}


def compare_tonal_peaks(before: dict, after: dict) -> dict:
    bin_tolerance_hz = before["sample_rate_hz"] / FFT_SIZE * 1.5
    before_frequencies = [peak["frequency_hz"] for peak in before["stable_tonal_peaks"]]
    new_peaks = [
        peak
        for peak in after["stable_tonal_peaks"]
        if all(abs(peak["frequency_hz"] - frequency) > bin_tolerance_hz for frequency in before_frequencies)
    ]
    return {
        "stable_peak_fraction_threshold": STABLE_TONE_FRACTION,
        "frequency_tolerance_hz": round(bin_tolerance_hz, 2),
        "new_stable_tonal_peaks": new_peaks,
        "passes": not new_peaks,
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--before", action="append", required=True, help="pre-change participant WAV (repeatable)")
    parser.add_argument("--after", action="append", required=True, help="post-change participant WAV (repeatable)")
    return parser.parse_args()


def analyze_group(paths: list[str]) -> tuple[list[dict], dict]:
    files = [analyze_file(pathlib.Path(path)) for path in paths]
    return [public_file(item) for item in files], aggregate(files)


def main() -> int:
    args = parse_args()
    try:
        before_files, before_aggregate = analyze_group(args.before)
        after_files, after_aggregate = analyze_group(args.after)
        result = {
            "analysis": {
                "frame_duration_ms": FRAME_DURATION_MS,
                "silence_floor_dbfs": SILENCE_FLOOR_DBFS,
                "stable_tone_definition": "same FFT bin is the dominant quiet-frame peak in at least 10% of full quiet frames",
            },
            "before": {"files": before_files, "aggregate": before_aggregate},
            "after": {"files": after_files, "aggregate": after_aggregate},
            "comparison": compare_tonal_peaks(before_aggregate, after_aggregate),
        }
    except ValueError as error:
        print(error, file=sys.stderr)
        return 2
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
