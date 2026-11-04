#!/usr/bin/env python3
"""Approximate a short monophonic clip (mp3/wav) with Flipper buzzer tones.

  soundfit.py in.mp3 out-basename [--notes N]

Decodes with ffmpeg, detects N note segments (pitch + boundaries) from the spectrogram,
then refines start/end/frequency by coordinate search, minimising the distance between
log spectrograms of the clip and of a synthesized square-wave version (what the Flipper's
speaker produces). Writes:
  out.tones  fpager tone file: "<freq_hz> <duration_ms>" per line (0 Hz = rest), Hz chosen so
             the Flipper's 128 kHz/N speaker timer lands on the nearest playable pitch
  out.mid    standard MIDI file (nearest semitones)
  out.wav    the square-wave rendering, to listen to on the host
Needs numpy and scipy. Then fine-tune on the real speaker with tools/micloop.py.
"""
import argparse, os, struct, subprocess, sys, tempfile
import numpy as np
from scipy.io import wavfile
from scipy.signal import stft

SR, HOP = 22050, 110


def load(path):
    with tempfile.TemporaryDirectory() as d:
        wav = os.path.join(d, "in.wav")
        subprocess.run(["ffmpeg", "-v", "error", "-y", "-i", path, "-ac", "1", "-ar", str(SR), wav], check=True)
        _, x = wavfile.read(wav)
    x = x.astype(float)
    return x / (np.abs(x).max() or 1)


def spec(sig):
    f, _, Z = stft(sig, SR, nperseg=1024, noverlap=1024 - HOP)
    band = (f > 150) & (f < 8000)
    A = np.abs(Z[band])
    return f[band], np.log10(A + 1e-3), np.sqrt((A ** 2).sum(0))


def synth(notes, n):
    y = np.zeros(n)
    for s, e, fr in notes:
        a, b = int(s * SR), int(e * SR)
        if fr > 0:
            y[a:b] = np.sign(np.sin(2 * np.pi * fr * np.arange(b - a) / SR + 1e-9))
    return y


def initial(x, count):
    """Segment frames by dominant pitch (semitone) into runs; keep the longest voiced runs."""
    f, S, E = spec(x)
    E = E / E.max()
    t = np.arange(len(E)) * HOP / SR
    pitch = np.where(E > 0.05, f[np.argmax(S, 0)], 0)
    semi = np.where(pitch > 0, np.round(12 * np.log2(np.maximum(pitch, 1) / 440)), -999)
    runs, start = [], 0
    for i in range(1, len(semi) + 1):
        if i == len(semi) or semi[i] != semi[start]:
            if semi[start] != -999 and i - start >= 3:
                runs.append([t[start], t[i - 1] + HOP / SR, float(np.median(pitch[start:i]))])
            start = i
    runs.sort(key=lambda r: r[1] - r[0], reverse=True)
    runs = sorted(runs[:count])
    for a, b in zip(runs, runs[1:]):  # close small gaps
        if b[0] - a[1] < 0.02:
            a[1] = b[0]
    return runs


def fit(x, notes):
    _, Sx, Ex = spec(x)
    Ex = Ex / Ex.max()
    on = Ex > 0.05

    def loss(ns):
        _, Sy, Ey = spec(synth(ns, len(x)))
        Ey = Ey / (Ey.max() or 1)
        a, b = Sx - Sx.mean(0), Sy - Sy.mean(0)
        return float(((a - b)[:, on] ** 2).mean() + 5 * (on != (Ey > 0.05)).mean())

    best = loss(notes)
    print(f"initial loss {best:.4f}", file=sys.stderr)
    for rnd in range(3):
        for dt, df in ((0.004, 5.0), (0.002, 2.0), (0.001, 1.0)):
            improved = True
            while improved:
                improved = False
                for i in range(len(notes)):
                    for j, d in ((0, dt), (1, dt), (2, df)):
                        for sgn in (-1, 1):
                            c = [n[:] for n in notes]
                            c[i][j] += sgn * d
                            if j == 1 and i + 1 < len(c) and abs(c[i + 1][0] - notes[i][1]) < 1e-9:
                                c[i + 1][0] = c[i][1]
                            if j == 0 and i > 0 and abs(c[i - 1][1] - notes[i][0]) < 1e-9:
                                c[i - 1][1] = c[i][0]
                            if any(n[1] - n[0] < 0.01 for n in c):
                                continue
                            l = loss(c)
                            if l < best - 1e-6:
                                best, notes, improved = l, c, True
        print(f"round {rnd} loss {best:.4f}", file=sys.stderr)
    return notes


# Flipper speaker: TIM16 at 64 MHz / 500 = 128 kHz, period = floor(128000 / f) ticks
# (furi_hal_speaker.c), so only 128000/N Hz is playable and requests round *up* in pitch.
SPEAKER_CLK = 128000


def flipper_request(freq):
    """Requested Hz that makes the speaker play the grid pitch nearest to freq."""
    n = max(3, round(SPEAKER_CLK / freq))
    return round(SPEAKER_CLK / (n + 0.25))


def write_tones(path, notes):
    t, lines = 0.0, ["# fpager tones: <freq_hz> <duration_ms> [volume]; 0 Hz = rest; Hz pre-compensated for the speaker timer"]
    t = notes[0][0]
    for s, e, fr in notes:
        if s - t >= 0.005:
            lines.append(f"0 {round((s - t) * 1000)}")
        lines.append(f"{flipper_request(fr)} {round((e - s) * 1000)}")
        t = e
    open(path, "w").write("\n".join(lines) + "\n")


def write_midi(path, notes, bpm=120):
    tpq = 480
    us_per_q = int(60e6 / bpm)
    ticks = lambda sec: int(round(sec * 1e6 / us_per_q * tpq))
    def vlq(n):
        out = [n & 0x7F]
        while n > 0x7F:
            n >>= 7
            out.append((n & 0x7F) | 0x80)
        return bytes(reversed(out))
    ev, t = bytearray(), notes[0][0]
    ev += vlq(0) + b"\xff\x51\x03" + us_per_q.to_bytes(3, "big")
    ev += vlq(0) + b"\xc0\x50"  # program: square lead
    for s, e, fr in notes:
        key = int(round(69 + 12 * np.log2(fr / 440)))
        ev += vlq(ticks(s - t)) + bytes([0x90, key, 100])
        ev += vlq(ticks(e - s)) + bytes([0x80, key, 0])
        t = e
    ev += vlq(0) + b"\xff\x2f\x00"
    data = b"MThd" + struct.pack(">IHHH", 6, 0, 1, tpq) + b"MTrk" + struct.pack(">I", len(ev)) + bytes(ev)
    open(path, "wb").write(data)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("input")
    ap.add_argument("out")
    ap.add_argument("--notes", type=int, default=5)
    a = ap.parse_args()
    x = load(a.input)
    notes = fit(x, initial(x, a.notes))
    write_tones(a.out + ".tones", notes)
    write_midi(a.out + ".mid", notes)
    wavfile.write(a.out + ".wav", SR, (0.5 * synth(notes, len(x)) * 32767).astype(np.int16))
    for s, e, fr in notes:
        print(f"{s:.3f}-{e:.3f}s {1000 * (e - s):4.0f} ms {fr:7.1f} Hz midi {69 + 12 * np.log2(fr / 440):6.2f}")


if __name__ == "__main__":
    main()
