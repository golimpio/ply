import type { Method } from './api';

export function formatDuration(sec?: number): string {
  if (!sec || sec <= 0) return '';
  if (sec < 60) return `${Math.round(sec)}s`;
  const total = Math.round(sec / 60);
  const h = Math.floor(total / 60);
  const m = total % 60;
  return h ? `${h}h ${m}m` : `${m}m`;
}

export function formatClock(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = String(s % 60).padStart(2, '0');
  return h ? `${h}:${String(m).padStart(2, '0')}:${ss}` : `${m}:${ss}`;
}

export function formatBytes(n?: number): string {
  if (!n || n <= 0) return '';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

export function formatBitrate(bps?: number): string {
  if (!bps || bps <= 0) return '';
  return bps >= 1e6 ? `${(bps / 1e6).toFixed(1)} Mbps` : `${Math.round(bps / 1e3)} kbps`;
}

/** Resolution badge. Width is checked too: a 1920x800 scope film is still "1080p". */
export function resolutionLabel(width?: number, height?: number): string {
  const w = width ?? 0;
  const h = height ?? 0;
  if (w >= 3400 || h >= 2000) return '4K';
  if (w >= 2500 || h >= 1400) return '1440p';
  if (w >= 1800 || h >= 1000) return '1080p';
  if (w >= 1200 || h >= 700) return '720p';
  return h > 0 ? 'SD' : '';
}

export function hdrLabel(hdr?: boolean | string | null): string {
  if (!hdr) return '';
  if (hdr === true) return 'HDR';
  const s = hdr.toLowerCase();
  if (s === 'sdr' || s === 'none' || s === 'false') return '';
  if (s.includes('dolby') || s === 'dv' || s.includes('dovi')) return 'Dolby Vision';
  if (s.includes('hlg')) return 'HLG';
  if (s.includes('hdr10+')) return 'HDR10+';
  if (s.includes('hdr10')) return 'HDR10';
  return s.toUpperCase();
}

export function episodeLabel(season?: number, episode?: number): string {
  const s = String(season ?? 0).padStart(2, '0');
  const e = String(episode ?? 0).padStart(2, '0');
  return `S${s}E${e}`;
}

export function seasonLabel(season: number): string {
  return season === 0 ? 'Specials' : `Season ${season}`;
}

const CODEC_NAMES: Record<string, string> = {
  h264: 'H.264',
  hevc: 'HEVC',
  vp9: 'VP9',
  av1: 'AV1',
  aac: 'AAC',
  ac3: 'AC-3',
  eac3: 'E-AC-3',
  mp3: 'MP3',
  opus: 'Opus',
  flac: 'FLAC',
  dts: 'DTS',
  truehd: 'TrueHD',
};

export function codecLabel(codec?: string): string {
  if (!codec) return '';
  return CODEC_NAMES[codec.toLowerCase()] ?? codec.toUpperCase();
}

export function channelLabel(n?: number): string {
  if (!n) return '';
  const known: Record<number, string> = { 1: 'mono', 2: 'stereo', 6: '5.1', 8: '7.1' };
  return known[n] ?? `${n}ch`;
}

export function methodLabel(m: Method): string {
  switch (m) {
    case 'direct':
      return 'Direct play';
    case 'remux':
      return 'Remux (HLS)';
    case 'transcode':
      return 'Transcode (HLS)';
    default:
      return m;
  }
}

export function initials(title: string): string {
  const words = title.match(/[\p{L}\p{N}]+/gu) ?? [];
  return words
    .slice(0, 2)
    .map((w) => w[0])
    .join('')
    .toUpperCase();
}

export function titleHue(title: string): number {
  let h = 0;
  for (let i = 0; i < title.length; i++) h = (h * 31 + title.charCodeAt(i)) >>> 0;
  return h % 360;
}
