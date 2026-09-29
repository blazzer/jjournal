const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches

document.addEventListener("keydown", (event) => {
  if (event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement) {
    return
  }
  const cards = [...document.querySelectorAll("article")]
  if (cards.length === 0) {
    return
  }
  const current = cards.findIndex((card) => card === document.activeElement)
  if (event.key === "j" || event.key === "k") {
    event.preventDefault()
    const next = event.key === "j" ? Math.min(current + 1, cards.length - 1) : Math.max(current - 1, 0)
    const card = cards[next < 0 ? 0 : next]
    card.tabIndex = 0
    card.focus({ preventScroll: reduced })
  }
})
