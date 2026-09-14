// Subsequence fuzzy match for the command palette. Prefers matches at word
// starts and contiguous runs; returns the matched positions for highlighting.

export interface FuzzyMatch {
  score: number;
  positions: number[];
}

export function fuzzy(query: string, text: string): FuzzyMatch | null {
  const q = query.trim().toLowerCase();
  if (!q) return { score: 0, positions: [] };
  const t = text.toLowerCase();
  const positions: number[] = [];
  let score = 0;
  let ti = 0;
  let prev = -2;
  for (let qi = 0; qi < q.length; qi++) {
    const ch = q[qi]!;
    const idx = t.indexOf(ch, ti);
    if (idx < 0) return null;
    positions.push(idx);
    // contiguous run
    if (idx === prev + 1) score += 3;
    // word start
    if (idx === 0 || /[\s\-_./:]/.test(t[idx - 1] ?? "")) score += 2;
    // earlier is better
    score += Math.max(0, 1 - idx / 40);
    prev = idx;
    ti = idx + 1;
  }
  // exact substring bonus
  if (t.includes(q)) score += 4 + (t.startsWith(q) ? 3 : 0);
  return { score, positions };
}

/** Splits text into [chunk, matched] pairs for rendering highlights. */
export function segments(text: string, positions: number[]): { text: string; hit: boolean }[] {
  if (!positions.length) return [{ text, hit: false }];
  const set = new Set(positions);
  const out: { text: string; hit: boolean }[] = [];
  let cur = "";
  let curHit = set.has(0);
  for (let i = 0; i < text.length; i++) {
    const hit = set.has(i);
    if (hit !== curHit) {
      if (cur) out.push({ text: cur, hit: curHit });
      cur = "";
      curHit = hit;
    }
    cur += text[i];
  }
  if (cur) out.push({ text: cur, hit: curHit });
  return out;
}
