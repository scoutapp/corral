// A short two-note WebAudio chime (no bundled asset). Created lazily on first
// use because browsers only start an AudioContext after a user gesture — the
// first click anywhere unlocks it. Best-effort: never throws into the caller.
let audioCtx: AudioContext | null = null;

export function chime(): void {
  try {
    if (!audioCtx) {
      const AC = window.AudioContext || (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
      if (!AC) return;
      audioCtx = new AC();
    }
    if (audioCtx.state === "suspended") void audioCtx.resume();
    const now = audioCtx.currentTime;
    [880, 1174.66].forEach((freq, i) => {
      const osc = audioCtx!.createOscillator();
      const gain = audioCtx!.createGain();
      osc.type = "sine";
      osc.frequency.value = freq;
      const t = now + i * 0.16;
      gain.gain.setValueAtTime(0.0001, t);
      gain.gain.exponentialRampToValueAtTime(0.2, t + 0.02);
      gain.gain.exponentialRampToValueAtTime(0.0001, t + 0.35);
      osc.connect(gain).connect(audioCtx!.destination);
      osc.start(t);
      osc.stop(t + 0.4);
    });
  } catch {
    /* audio is best-effort; never break the page */
  }
}
