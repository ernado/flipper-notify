#!/usr/bin/env python3
"""Tune a .tones file on the real Flipper speaker: play it over USB (fpager probe -play, no
vibration), record with a microphone (pw-record), measure each note (harmonics 1-3 energy, start,
length, pitch), correct durations and per-note volume towards the original clip, repeat.
Writes <workdir>/best.tones. Recordings are deleted after analysis. Needs numpy, scipy, ffmpeg-free
wav input (convert the clip with ffmpeg first), user in dialout, daemon not needed.
Pitch is not tuned: the speaker plays 128000/N Hz only (see soundfit.py)."""
import json, os, subprocess, sys, time
import numpy as np
from scipy.io import wavfile
from scipy.signal import stft

import argparse
ap = argparse.ArgumentParser(description=__doc__)
ap.add_argument("workdir"); ap.add_argument("original", help="original clip (wav)"); ap.add_argument("start", help="starting .tones")
ap.add_argument("--iters", type=int, default=6); ap.add_argument("--mic", default=os.environ.get("FPAGER_MIC"), required=not os.environ.get("FPAGER_MIC"), help="PipeWire source name (pactl list sources short)")
ap.add_argument("--fpager", default="fpager")
A = ap.parse_args(); S, ORIG, START, ITERS = A.workdir, A.original, A.start, A.iters
MIC = A.mic
FP = A.fpager

def read_tones(p):
    out = []
    for ln in open(p):
        ln = ln.split("#")[0].split()
        if len(ln) >= 2:
            out.append([float(ln[0]), float(ln[1]), float(ln[2]) if len(ln) > 2 else 1.0])
    return out

def write_tones(p, tones):
    with open(p, "w") as f:
        f.write("# fpager tones: <freq_hz> <duration_ms> [volume]\n")
        for fr, ms, v in tones:
            f.write(f"{round(fr)} {round(ms)} {v:.3f}\n")

def load(p, sr=48000):
    r, x = wavfile.read(p)
    x = x.astype(float)
    if x.ndim > 1: x = x.mean(1)
    if r != sr:
        raise SystemExit(f"unexpected rate {r}")
    return x / 32768.0

def measure(x, sr, tones):
    """Segment the note sequence. Per frame (2.5 ms) and per semitone group: energy summed over
    harmonics 1..3 (±2.5%). Frames above noise floor + 12 dB get the dominant group's label
    (mode-filtered); notes are matched in order, tolerating gaps of up to 3 frames."""
    semi = lambda fr: int(round(12 * np.log2(fr / 440)))
    freqs = [fr for fr, _, _ in tones]
    groups = sorted(set(semi(fr) for fr in freqs))
    f, t, Z = stft(x, sr, nperseg=1024, noverlap=1024 - 120)
    A = np.abs(Z) ** 2
    E = []
    for g in groups:
        c = 440 * 2 ** (g / 12)
        m = np.zeros(len(f), bool)
        for h in (1, 2, 3):
            m |= (f > c * h * 0.975) & (f < c * h * 1.025)
        E.append(10 * np.log10(A[m].sum(0) + 1e-12))
    E = np.array(E)
    tot = E.max(0)
    floor = np.percentile(tot, 20)
    loud = tot > floor + 12
    lab = np.where(loud, np.array(groups)[E.argmax(0)], -999)
    sm = lab.copy()
    for i in range(2, len(lab) - 2):
        w = lab[i - 2:i + 3]
        vals, cnt = np.unique(w, return_counts=True)
        sm[i] = vals[cnt.argmax()]
    segs, i, n = [], 0, len(t)
    for fr in freqs:
        g = semi(fr)
        while i < n and sm[i] != g: i += 1
        a, last, gap = i, i, 0
        while i < n and gap <= 3:
            if sm[i] == g: last, gap = i, 0
            elif sm[i] in groups and sm[i] != g: break
            else: gap += 1
            i += 1
        i = last + 1
        if last - a < 2:
            return None
        seg = slice(a, last + 1)
        spec = A[:, seg].sum(1); band = (f > fr * 0.95) & (f < fr * 1.05)
        k = int(np.argmax(np.where(band, spec, 0)))
        pitch = f[k]
        if 0 < k < len(f) - 1:
            a1, b1, c1 = (np.log(spec[j] + 1e-12) for j in (k - 1, k, k + 1))
            d = a1 - 2 * b1 + c1
            if d != 0: pitch = f[k] + 0.5 * (a1 - c1) / d * (f[1] - f[0])
        gi = groups.index(g)
        segs.append(dict(t0=t[a], ms=(t[last] - t[a]) * 1000 + 2.5, db=float(np.median(E[gi, seg])), hz=float(pitch)))
    return segs

def record_play(tones_path, out_wav):
    rec = subprocess.Popen(["pw-record", "--target", MIC, "--rate", "48000", "--channels", "1", "--format", "s16", out_wav])
    time.sleep(0.6)
    subprocess.run([FP, "probe", "-transport", "usb", "-play", tones_path], check=True, capture_output=True)
    time.sleep(1.6)
    rec.send_signal(2); rec.wait()

# Targets from the original clip.
sr_o, xo = wavfile.read(ORIG); xo = xo.astype(float); xo = xo.mean(1) if xo.ndim > 1 else xo
tones = read_tones(START)
seg_o = None
# resample original to 48k for identical analysis
from scipy.signal import resample_poly
xo48 = resample_poly(xo, 48000, sr_o) / (np.abs(xo).max() or 1)
seg_o = measure(xo48, 48000, tones)
assert seg_o, "could not segment original"
tgt_rel = np.array([s["db"] for s in seg_o]); tgt_rel -= tgt_rel.max()
print("original:", [(round(s["ms"]), round(s["hz"]), round(r, 1)) for s, r in zip(seg_o, tgt_rel)])

hist = []
for it in range(ITERS):
    cand = os.path.join(S, f"cand{it}.tones"); wav = os.path.join(S, f"rec{it}.wav")
    write_tones(cand, tones)
    record_play(cand, wav)
    x = load(wav)
    segs = measure(x, 48000, tones)
    os.remove(wav)
    if not segs:
        print(f"iter {it}: could not segment recording (too quiet?)"); break
    rel = np.array([s["db"] for s in segs]); rel -= rel.max()
    dur_err = [s["ms"] - so["ms"] for s, so in zip(segs, seg_o)]
    hz_err = [s["hz"] - so["hz"] for s, so in zip(segs, seg_o)]
    lvl_err = rel - tgt_rel
    score = float(np.sqrt(np.mean(np.square(dur_err)) / 25 + np.mean(np.square(lvl_err)) / 9))
    hist.append((score, [t[:] for t in tones]))
    print(f"iter {it}: score {score:.3f}  dur_err_ms {np.round(dur_err,1).tolist()}  lvl_err_db {np.round(lvl_err,1).tolist()}  hz_err {np.round(hz_err,1).tolist()}  peak_db {max(s['db'] for s in segs):.1f}")
    # corrections (damped)
    for i, (s, so) in enumerate(zip(segs, seg_o)):
        tones[i][1] = max(10, tones[i][1] - 0.7 * (s["ms"] - so["ms"]))
        # volume: dB error -> multiplicative change; keep max note at 1.0
        tones[i][2] = float(np.clip(tones[i][2] * 10 ** (-0.5 * lvl_err[i] / 20), 0.05, 1.0))
    m = max(t[2] for t in tones)
    for t in tones: t[2] = min(1.0, t[2] / m)
best = min(hist, key=lambda h: h[0])
write_tones(os.path.join(S, "best.tones"), best[1])
print("best score", round(best[0], 3)); print(open(os.path.join(S, "best.tones")).read())
