///|
/// A content-free, allocation-free port of the Rust interrupt scheduler.
/// State is packed into one positive i64; -1L is the invalid-state result.
#export_name("advance_intentional_interrupt")
pub fn advance_intentional_interrupt(
  packed_state : Int64,
  frame_flags : Int,
  rms : Double,
  peak : Double,
  credited_ms : Int,
  candidate_elapsed_ms : Int,
  aec_verified : Int,
) -> Int64 {
  let mut phase = ((packed_state >> 0) & 7L).to_int()
  let mut score = ((packed_state >> 3) & 63L).to_int() - 24
  let mut foreground_ms = ((packed_state >> 9) & 4095L).to_int()
  let mut change_count = ((packed_state >> 21) & 15L).to_int()
  let mut gap_ms = ((packed_state >> 25) & 127L).to_int()
  let mut last_bucket = ((packed_state >> 32) & 7L).to_int()
  let last_elapsed_ms = ((packed_state >> 35) & 4095L).to_int()
  if packed_state < 0L ||
    phase > 6 ||
    score < -24 ||
    score > 32 ||
    gap_ms > 80 ||
    foreground_ms > 2400 ||
    foreground_ms > candidate_elapsed_ms ||
    change_count > 8 ||
    last_bucket > 4 ||
    last_elapsed_ms >= candidate_elapsed_ms ||
    (frame_flags & -8) != 0 ||
    rms.is_nan() ||
    rms < 0.0 ||
    rms > 1.0 ||
    peak.is_nan() ||
    peak < 0.0 ||
    peak > 1.0 ||
    credited_ms == 0 ||
    credited_ms > 40 ||
    candidate_elapsed_ms == 0 ||
    candidate_elapsed_ms > 2400 {
    return -1L
  }
  let mut signal = 0
  if phase == 5 || phase == 6 || phase == 4 {
    return pack_step(
      phase, score, foreground_ms, change_count, gap_ms, last_bucket, candidate_elapsed_ms,
      0, false,
    )
  }
  if aec_verified == 0 || candidate_elapsed_ms > 520 {
    phase = 5
    return pack_step(
      phase, score, foreground_ms, change_count, gap_ms, last_bucket, candidate_elapsed_ms,
      0, false,
    )
  }
  let guard_voiced = (frame_flags & 1) != 0
  let voiced = (frame_flags & 2) != 0
  let foreground = guard_voiced && (frame_flags & 4) != 0
  let bucket = if foreground {
    if rms >= 0.075 || peak >= 0.20 {
      4
    } else if rms >= 0.055 || peak >= 0.15 {
      3
    } else if rms >= 0.038 || peak >= 0.105 {
      2
    } else {
      1
    }
  } else {
    0
  }
  let delta = if bucket >= last_bucket {
    bucket - last_bucket
  } else {
    last_bucket - bucket
  }
  let changed = foreground && last_bucket != 0 && delta >= 2
  signal = if changed {
    3
  } else if foreground {
    2
  } else if voiced {
    1
  } else {
    0
  }
  if signal == 3 {
    score = int_min(score + 6, 32)
    change_count = int_min(change_count + 1, 8)
    foreground_ms = int_min(foreground_ms + credited_ms, 65535)
    gap_ms = 0
    last_bucket = bucket
  } else if signal == 2 {
    score = int_min(score + 2, 32)
    foreground_ms = int_min(foreground_ms + credited_ms, 65535)
    gap_ms = 0
    last_bucket = bucket
  } else if signal == 1 {
    score = int_max(score - 6, -24)
    phase = 5
  } else {
    score = int_max(score - 5, -24)
    let next_gap = int_min(gap_ms + credited_ms, 255)
    if next_gap > 80 || score <= -12 {
      phase = 6
      gap_ms = 80
    } else {
      gap_ms = next_gap
    }
  }
  if phase == 0 && foreground {
    phase = 1
  }
  if phase == 1 && foreground_ms >= 160 && change_count >= 1 && score >= 10 {
    phase = 2
  }
  let upper_boundary_reached = (phase == 1 || phase == 2 || phase == 3) &&
    foreground_ms >= 320 &&
    change_count >= 3 &&
    score >= 24 &&
    gap_ms <= 80
  if upper_boundary_reached {
    phase = 3
  }
  let fast_ready = phase == 3 &&
    candidate_elapsed_ms >= 400 &&
    candidate_elapsed_ms <= 520 &&
    foreground_ms >= 320 &&
    change_count >= 3 &&
    score >= 24 &&
    gap_ms <= 80
  if fast_ready {
    phase = 4
  }
  pack_step(
    phase, score, foreground_ms, change_count, gap_ms, last_bucket, candidate_elapsed_ms,
    signal, fast_ready,
  )
}

///|
fn int_min(a : Int, b : Int) -> Int {
  if a < b {
    a
  } else {
    b
  }
}

///|
fn int_max(a : Int, b : Int) -> Int {
  if a > b {
    a
  } else {
    b
  }
}

///|
fn pack_step(
  phase : Int,
  score : Int,
  foreground_ms : Int,
  change_count : Int,
  gap_ms : Int,
  last_bucket : Int,
  last_elapsed_ms : Int,
  signal : Int,
  fast_ready : Bool,
) -> Int64 {
  phase.to_int64() |
  ((score + 24).to_int64() << 3) |
  (foreground_ms.to_int64() << 9) |
  (change_count.to_int64() << 21) |
  (gap_ms.to_int64() << 25) |
  (last_bucket.to_int64() << 32) |
  (last_elapsed_ms.to_int64() << 35) |
  (signal.to_int64() << 47) |
  ((if fast_ready { 1L } else { 0L }) << 49)
}
