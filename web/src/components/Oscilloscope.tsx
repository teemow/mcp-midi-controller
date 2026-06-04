import { useEffect, useRef } from "react";

interface OscilloscopeProps {
  // active drives the amplitude/energy of the trace; bump it on activity (e.g.
  // a new log entry) to make the waveform pulse.
  energy?: number;
  className?: string;
}

// Oscilloscope renders an animated neon waveform on a canvas — the signalwave
// signature motif used in the app header and the activity feed.
export function Oscilloscope({ energy = 0, className }: OscilloscopeProps) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const energyRef = useRef(energy);
  energyRef.current = energy;

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    let raf = 0;
    let t = 0;
    let pulse = 0;

    const draw = () => {
      const dpr = window.devicePixelRatio || 1;
      const w = canvas.clientWidth;
      const h = canvas.clientHeight;
      if (canvas.width !== w * dpr || canvas.height !== h * dpr) {
        canvas.width = w * dpr;
        canvas.height = h * dpr;
      }
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, w, h);

      pulse = Math.max(0, pulse * 0.94);
      if (energyRef.current > 0) pulse = Math.min(1, pulse + 0.4);
      energyRef.current = 0;

      const mid = h / 2;
      const baseAmp = h * 0.18;
      const amp = baseAmp * (0.5 + pulse * 0.9);

      const trace = (offset: number, color: string, alpha: number) => {
        ctx.beginPath();
        for (let x = 0; x <= w; x += 2) {
          const k = x / w;
          const y =
            mid +
            Math.sin(k * Math.PI * 6 + t + offset) * amp * Math.sin(k * Math.PI) +
            Math.sin(k * Math.PI * 18 + t * 2.3 + offset) * amp * 0.25 * pulse;
          if (x === 0) ctx.moveTo(x, y);
          else ctx.lineTo(x, y);
        }
        ctx.strokeStyle = color;
        ctx.globalAlpha = alpha;
        ctx.lineWidth = 1.5;
        ctx.shadowColor = color;
        ctx.shadowBlur = 8;
        ctx.stroke();
        ctx.globalAlpha = 1;
        ctx.shadowBlur = 0;
      };

      trace(0, "#22d3ee", 0.9);
      trace(Math.PI, "#f472d0", 0.5);

      t += 0.04;
      raf = requestAnimationFrame(draw);
    };
    raf = requestAnimationFrame(draw);
    return () => cancelAnimationFrame(raf);
  }, []);

  return <canvas ref={canvasRef} className={className} />;
}
