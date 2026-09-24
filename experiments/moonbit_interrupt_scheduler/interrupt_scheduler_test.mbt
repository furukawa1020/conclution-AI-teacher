///|
test "initial foreground frame becomes candidate" {
  let initial = 24L << 3
  let result = @interrupt_scheduler.advance_intentional_interrupt(
    initial, 7, 0.08, 0.21, 40, 40, 1,
  )
  assert_eq((result & 7L).to_int(), 1)
  assert_eq(((result >> 47) & 3L).to_int(), 2)
  assert_eq(((result >> 9) & 4095L).to_int(), 40)
}

///|
test "loss of AEC delegates permanently to legacy" {
  let initial = 24L << 3
  let result = @interrupt_scheduler.advance_intentional_interrupt(
    initial, 7, 0.08, 0.21, 40, 40, 0,
  )
  assert_eq((result & 7L).to_int(), 5)
}

///|
test "invalid state returns the sole sentinel" {
  let initial = 24L << 3
  let result = @interrupt_scheduler.advance_intentional_interrupt(
    initial, 8, 0.08, 0.21, 40, 40, 1,
  )
  assert_eq(result, -1L)
}
