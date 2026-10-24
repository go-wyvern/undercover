type Capacity = 4 | 6

const positions: Record<Capacity, readonly (readonly [number, number])[]> = {
  4: [[50, 91], [10, 47], [50, 7], [90, 47]],
  6: [[50, 91], [12, 70], [12, 25], [50, 7], [88, 25], [88, 70]],
}

// Rotate the view around the table without changing any actual seat numbers.
export function seatPosition(seat: number, ownSeat: number, capacity: Capacity) {
  const index = ((seat - ownSeat) % capacity + capacity) % capacity
  const [left, top] = positions[capacity][index]!
  return { left: `${left}%`, top: `${top}%`, '--seat-index': index }
}

export function tableCardPosition(seat: number, ownSeat: number, capacity: Capacity) {
  const position = seatPosition(seat, ownSeat, capacity)
  const x = parseFloat(position.left)
  const y = parseFloat(position.top)
  return {
    left: `${x === 50 ? position['--seat-index'] === 0 ? 65 : 40 : 50 + (x - 50) * .65}%`,
    top: `${48 + (y - 48) * .66}%`,
    '--card-rotation': `${seat % 2 ? -7 : 7}deg`,
  }
}
