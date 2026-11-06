// A watched stream's playout: picture and sound on one clock (seconds: the
// audio context's, or the page's for a stream without sound), a lead behind
// the stream, so jitter on the way does not show. Watching is one-way, so a
// lead of half a second costs nothing.
//
// The first item (picture or sound) sets the clock: it plays the lead after
// it came. A late item raises the lead in steps, up to MAX_LEAD, and plays at
// once: a short pause, not a skip (what was already scheduled stays). After
// a calm period the lead goes down again (sound plays 2 % faster
// meanwhile, see rate; nobody hears that), while the slowest item since
// then still had more than MIN_LEAD to spare. Only items move it: at most
// IDLE seconds of drift per item, so a still screen share that sends one
// picture a minute still shows it.
export const MIN_LEAD = 0.5;
export const MAX_LEAD = 1.5;
const STEP = 0.25;
const CALM = 15; // seconds without a late item before the lead goes down
const DRIFT = 0.02; // seconds less lead per second
const STUCK = 1; // late this long at the most lead: a delay that stays, start over
const FAR_AHEAD = 1; // seconds beyond the lead: the clock jumped, start over
const IDLE = 1; // seconds between items that count for lowering, at the most

export const createPlayout = (minLead = MIN_LEAD) => {
  let offset: number | null = null; // stream time (s) + offset = play time
  let lead = minLead;
  let calmSince = 0;
  let slack = Infinity; // the least time to spare of an item since calmSince (none yet: Infinity)
  let last = 0;
  let lateSince: number | null = null;
  let lowering = false;
  let calmReached = false;
  const anchor = (t: number, now: number) => {
    offset = now + lead - t;
    calmSince = now;
    slack = Infinity;
    lateSince = null;
    calmReached = false;
  };
  return {
    // When to play an item with this timestamp (microseconds) on the clock
    // that reads now; null: too late, drop it.
    at: (timestamp: number, now: number): number | null => {
      const t = timestamp / 1e6;
      if (offset === null || t + offset > now + lead + FAR_AHEAD) anchor(t, now);
      if (now - calmSince >= CALM && !calmReached) {
        calmReached = true;
        slack = Infinity;
      }
      // Without an item since the late one nothing is known: no lowering.
      lowering = calmReached && slack > minLead && slack < Infinity;
      if (lowering) {
        const d = Math.min(DRIFT * Math.min(IDLE, Math.max(0, now - last)), slack - minLead);
        offset! -= d;
        slack -= d;
        lead = Math.max(minLead, lead - d);
      }
      last = now;
      const when = t + offset!;
      if (when >= now) {
        lateSince = null;
        slack = Math.min(slack, when - now);
        return when;
      }
      const raise = Math.min(MAX_LEAD - lead, Math.ceil((now - when) / STEP) * STEP);
      lead += raise;
      offset! += raise;
      calmSince = now;
      slack = Infinity;
      calmReached = false;
      if (when + raise >= now) {
        lateSince = null;
        return when + raise;
      }
      lateSince ??= now;
      if (now - lateSince < STUCK) return null;
      anchor(t, now);
      return now + lead;
    },
    lead: () => lead,
    // How fast sound plays: a little faster while the lead goes down, or
    // its buffers would overlap.
    rate: () => (lowering ? 1 + DRIFT : 1),
    reset: () => {
      offset = null;
      lead = minLead;
      calmReached = false;
    },
  };
};

export type Playout = ReturnType<typeof createPlayout>;

// The newest of the queued items (play times or queue entries with when,
// oldest first) whose time came; the ones before it are skipped. -1: none yet.
export const lastDue = (times: readonly (number | { when: number })[], now: number): number => {
  let i = -1;
  while (i + 1 < times.length) {
    const item = times[i + 1]!;
    const t = typeof item === "number" ? item : item.when;
    if (t > now) break;
    i++;
  }
  return i;
};
