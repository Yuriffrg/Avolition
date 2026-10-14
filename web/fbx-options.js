// This module owns the options popover only. Export state belongs to app.js.
const button = document.getElementById('fbx-options-button');
const popover = document.getElementById('fbx-options');
let holdTimer = 0;
let suppressClick = false;
let origin = null;

function cancelHold() {
  clearTimeout(holdTimer);
  holdTimer = 0;
  origin = null;
}

function placePopover() {
  const anchor = button.getBoundingClientRect();
  const width = popover.offsetWidth || 236;
  const height = popover.offsetHeight || 222;
  const margin = 10;
  const below = anchor.bottom + 8;
  const top = below + height <= window.innerHeight - margin ? below : anchor.top - height - 8;
  popover.style.left = `${Math.max(margin, Math.min(anchor.right - width, window.innerWidth - width - margin))}px`;
  popover.style.top = `${Math.max(margin, Math.min(top, window.innerHeight - height - margin))}px`;
}

button.addEventListener('pointerdown', event => {
  if (event.button !== 0 || button.disabled) return;
  cancelHold();
  suppressClick = false;
  origin = {x: event.clientX, y: event.clientY};
  holdTimer = setTimeout(() => {
    holdTimer = 0;
    suppressClick = true;
    if (!popover.matches(':popover-open')) popover.showPopover();
    placePopover();
  }, 450);
});
button.addEventListener('pointermove', event => {
  if (origin && Math.hypot(event.clientX - origin.x, event.clientY - origin.y) > 9) cancelHold();
});
button.addEventListener('pointerleave', cancelHold);
window.addEventListener('pointerup', () => {
  cancelHold();
  // The click immediately after a completed hold must not toggle it closed.
  setTimeout(() => { suppressClick = false; }, 0);
});
window.addEventListener('pointercancel', () => { cancelHold(); suppressClick = false; });
button.addEventListener('click', event => {
  if (suppressClick) { event.preventDefault(); suppressClick = false; }
});
popover.addEventListener('beforetoggle', event => {
  button.setAttribute('aria-expanded', String(event.newState === 'open'));
  if (event.newState === 'open') placePopover();
});
popover.addEventListener('toggle', event => {
  button.setAttribute('aria-expanded', String(event.newState === 'open'));
  if (event.newState === 'open') placePopover();
});
window.addEventListener('resize', () => { if (popover.matches(':popover-open')) placePopover(); });
// Connection feedback can change the popover's height after it opens.
new ResizeObserver(() => { if (popover.matches(':popover-open')) placePopover(); }).observe(popover);
document.querySelector('.inspector-panel').addEventListener('scroll', () => {
  if (popover.matches(':popover-open')) placePopover();
}, {passive: true});
