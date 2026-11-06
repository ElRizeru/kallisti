import { describe, expect, it } from "vitest";
import { MAX_LEAD, MIN_LEAD, createPlayout, lastDue } from "../src/lib/stream/playout";

const s = (seconds: number) => seconds * 1e6; // stream timestamps are microseconds

describe("createPlayout", () => {
  it("plays the first item and the ones after it half a second after it came", () => {
    const p = createPlayout();
    expect(p.at(s(1), 10)).toBeCloseTo(10 + MIN_LEAD);
    expect(p.at(s(1.02), 10.01)).toBeCloseTo(10.52); // early: waits
    expect(p.at(s(1.04), 10.3)).toBeCloseTo(10.54); // jitter within the lead: on time
  });

  it("starts over cleanly when the audio clock runs again after it stood still (sound dropped meanwhile)", () => {
    const p = createPlayout();
    p.at(s(1), 10);
    // Pictures go on while the clock stands still at 10.
    expect(p.at(s(30), 10)).toBeCloseTo(10 + MIN_LEAD);
    // The clock runs again from where it stood; sound comes after a gap.
    expect(p.at(s(60), 10.02)).toBeCloseTo(10.02 + MIN_LEAD);
    expect(p.at(s(60.02), 10.04)).toBeCloseTo(10.04 + MIN_LEAD);
    expect(p.lead()).toBe(MIN_LEAD);
  });

  it("raises the lead in steps for a late item, which then plays at once (a pause, not a skip)", () => {
    const p = createPlayout();
    p.at(s(1), 10);
    const when = p.at(s(1.1), 10.7)!; // due at 10.6, 0.1 s late
    expect(p.lead()).toBeCloseTo(0.75);
    expect(when).toBeCloseTo(10.85);
    expect(when).toBeGreaterThanOrEqual(10.7);
    expect(p.at(s(1.12), 10.71)).toBeCloseTo(10.87); // the items after it follow on the new clock
  });

  it("raises by more steps for a longer stall, up to the most", () => {
    const p = createPlayout();
    p.at(s(1), 10);
    expect(p.at(s(1), 11.1)).toBeCloseTo(11.25); // 0.6 s late: three steps
    expect(p.lead()).toBeCloseTo(1.25);
    p.at(s(2), 12.6); // 0.35 s late again: capped
    expect(p.lead()).toBe(MAX_LEAD);
  });

  it("plays an item late by just over a step on time", () => {
    const p = createPlayout();
    p.at(s(1), 10);
    expect(p.at(s(1), 10.75 + 1e-12)).not.toBeNull();
  });

  it("drops items it cannot make on time at the most lead", () => {
    const p = createPlayout();
    p.at(s(1), 10);
    expect(p.at(s(1.1), 12)).toBeNull(); // 1.4 s late: more than the lead can grow
    expect(p.lead()).toBe(MAX_LEAD);
    expect(p.at(s(1.2), 12.01)).toBeNull();
  });

  it("starts the clock over when items stay late (a delay that stays)", () => {
    const p = createPlayout();
    p.at(s(0), 0);
    let last: number | null = null;
    for (let i = 0; i <= 60; i++) last = p.at(s(i * 0.02), 3 + i * 0.02); // 3 s late each
    expect(last).not.toBeNull();
    expect(last! - (3 + 60 * 0.02)).toBeCloseTo(MAX_LEAD);
  });

  it("starts over when an item would play far ahead (the clock jumped)", () => {
    const p = createPlayout();
    p.at(s(5), 10);
    expect(p.at(s(5.02), 7)).toBeCloseTo(7 + MIN_LEAD);
  });

  it("lowers the lead slowly after a calm period, not below the least", () => {
    const p = createPlayout();
    p.at(s(0), 0);
    p.at(s(0), 1); // 0.5 s late: lead 1
    expect(p.lead()).toBeCloseTo(1);
    let t = 1;
    const step = (until: number) => {
      for (; t < until; t += 0.02) expect(p.at(s(t), t)).not.toBeNull();
    };
    step(15);
    expect(p.lead()).toBeCloseTo(1); // not yet calm
    expect(p.rate()).toBe(1);
    step(20);
    expect(p.lead()).toBeLessThan(1);
    expect(p.lead()).toBeGreaterThan(0.95); // sound plays 0.5 % faster, unheard
    expect(p.rate()).toBeCloseTo(1.005);
    step(150);
    expect(p.lead()).toBeCloseTo(MIN_LEAD);
    expect(p.rate()).toBe(1);
    expect(p.at(s(t), t)! - t).toBeCloseTo(MIN_LEAD, 2); // the items moved with it
  });

  it("allows setting a custom minLead", () => {
    const p = createPlayout(0.3);
    expect(p.lead()).toBeCloseTo(0.3);
    expect(p.at(s(1), 10)).toBeCloseTo(10.3);
  });

  it("does not lower the lead into the jitter it still sees", () => {
    const p = createPlayout();
    p.at(s(0), 0);
    p.at(s(0), 1); // lead 1
    for (let t = 1; t < 300; t += 0.02) {
      const spike = Math.round(t * 50) % 50 === 0 ? 0.45 : 0; // once a second
      expect(p.at(s(t), t + spike)).not.toBeNull();
    }
    expect(p.lead()).toBeGreaterThan(0.6);
    expect(p.lead()).toBeLessThan(1);
  });

  it("skips to the live picture when the delay that made it start over goes away", () => {
    const p = createPlayout();
    p.at(s(0), 0);
    for (let i = 0; i <= 60; i++) p.at(s(i * 0.02), 3 + i * 0.02); // stuck 3 s late: a new start
    expect(p.at(s(4.3), 4.3)! - 4.3).toBeCloseTo(MAX_LEAD); // the delay is gone
  });

  it("drifts a small excess away after a calm minute", () => {
    const p = createPlayout();
    p.at(s(0), 0);
    p.at(s(0), 1.5); // 1 s late: lead 1.5
    let t = 1.5;
    for (; t < 400; t += 0.02) p.at(s(t + 0.6), t); // 0.6 s faster from now on: 2.1 s ahead
    expect(p.at(s(t + 0.6), t)! - t).toBeCloseTo(MIN_LEAD, 2);
  });

  it("keeps showing a sparse share's pictures after a late one (time alone moves nothing)", () => {
    const p = createPlayout();
    expect(p.at(0, 0)).toBeCloseTo(0.5);
    expect(p.at(125000, 0.75)).toBeCloseTo(0.875); // late: lead 0.75
    expect(p.at(300125000, 300.75)).toBeCloseTo(300.875); // 5 minutes of a still screen, same clocks
    for (let t = 330; t < 900; t += 30) expect(p.at(s(t - 0.625), t)).not.toBeNull(); // one change every 30 s
  });

  it("forgets everything on reset", () => {
    const p = createPlayout();
    p.at(s(0), 0);
    p.at(s(0), 1);
    p.reset();
    expect(p.lead()).toBe(MIN_LEAD);
    expect(p.at(s(9), 20)).toBeCloseTo(20 + MIN_LEAD);
  });

  it("recovers lead after a network stall and burst of queued items", () => {
    const p = createPlayout();
    p.at(s(0), 0);
    for (let t = 0.02; t <= 1; t += 0.02) p.at(s(t), t);
    p.at(s(1.02), 3.0);
    for (let t = 1.04; t <= 2.98; t += 0.02) p.at(s(t), 3.001);
    expect(p.lead()).toBe(MAX_LEAD);
    let now = 3.001;
    for (let t = 3.0; t <= 250; t += 0.02, now += 0.02) p.at(s(t), now);
    expect(p.lead()).toBeCloseTo(MIN_LEAD, 2);
  });
});

describe("lastDue", () => {
  it("is the newest item whose time came (older ones are skipped), or -1", () => {
    expect(lastDue([1, 2, 3], 2.5)).toBe(1);
    expect(lastDue([1, 2, 3], 0.5)).toBe(-1);
    expect(lastDue([], 5)).toBe(-1);
    expect(lastDue([1, 2], 9)).toBe(1);
  });

  it("handles queue entries with a when property without extra mapping allocations", () => {
    expect(lastDue([{ when: 1 }, { when: 2 }, { when: 3 }], 2.5)).toBe(1);
    expect(lastDue([{ when: 1 }, { when: 2 }, { when: 3 }], 0.5)).toBe(-1);
    expect(lastDue([], 5)).toBe(-1);
    expect(lastDue([{ when: 1 }, { when: 2 }], 9)).toBe(1);
  });
});
