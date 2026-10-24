import { test } from 'node:test'
import assert from 'node:assert/strict'
import { seatPosition, tableCardPosition } from './tableLayout.ts'

for (const capacity of [4, 6]) {
  for (let ownSeat = 1; ownSeat <= capacity; ownSeat++) {
    test(`${capacity} seats: player ${ownSeat} sits at the front with cards and turn order aligned`, () => {
      const own = seatPosition(ownSeat, ownSeat, capacity)
      assert.equal(own.left, '50%')
      assert.equal(own.top, '91%')
      assert.equal(own['--seat-index'], 0)
      const places = new Set()
      for (let step = 0; step < capacity; step++) {
        const seat = ((ownSeat - 1 + step) % capacity) + 1
        const position = seatPosition(seat, ownSeat, capacity)
        assert.equal(position['--seat-index'], step)
        places.add(`${position.left}/${position.top}`)
      }
      assert.equal(places.size, capacity)
      assert.equal(tableCardPosition(ownSeat, ownSeat, capacity).left, '65%')
      assert.equal(tableCardPosition(ownSeat, ownSeat, capacity).top, tableCardPosition(1, 1, capacity).top)
      const opposite = ((ownSeat - 1 + capacity / 2) % capacity) + 1
      assert.equal(seatPosition(opposite, ownSeat, capacity).top, '7%')
      assert.equal(tableCardPosition(opposite, ownSeat, capacity).left, '40%')
    })
  }
}
