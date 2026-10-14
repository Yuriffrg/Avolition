import assert from 'node:assert/strict';
import { MOUSE, PerspectiveCamera } from '../web/vendor/three.module.js';
import { OrbitControls } from '../web/vendor/OrbitControls.js';
import { PreviewOrbitControls } from '../web/navigation.js';

let checks = 0;
function close(actual, expected, label) {
  assert.ok(Math.abs(actual - expected) < 1e-10, `${label}: ${actual} != ${expected}`);
  checks++;
}
function controls(Type = PreviewOrbitControls) {
  const camera = new PerspectiveCamera();
  camera.position.set(0, 0, 10);
  const control = new Type(camera);
  control.mouseButtons.RIGHT = MOUSE.DOLLY;
  return control;
}
const distance = control => control.object.position.distanceTo(control.target);
function drag(control, events, button = 2) {
  control._onMouseDown({button, altKey: true, clientX: 100, clientY: 200});
  for (const [x, y] of events) control._onMouseMove({clientX: x, clientY: y, altKey: true});
  return distance(control);
}

const fast = controls();
assert.equal(fast.zoomSpeed, 3); checks++;
const horizontal = drag(fast, [[200, 200]]);
close(horizontal, 10 * Math.pow(.95, 3), 'Alt+RMB right drag zooms in by horizontal pixel magnitude');
close(drag(controls(), [[100, 400]]), 10, 'Vertical-only drag has no zoom effect');
close(drag(controls(), [[200, 400]]), horizontal, 'Diagonal drag ignores its vertical component');
close(drag(controls(), [[0, 200]]), 10 / Math.pow(.95, 3), 'Left drag zooms out');

const fine = drag(controls(), [[110, 200]]);
close(Math.log(horizontal / 10) / Math.log(fine / 10), 10, '100 pixels has ten times the logarithmic effect of 10 pixels');
const steps = Array.from({length: 10}, (_, i) => [110 + i * 10, 200]);
close(drag(controls(), steps), horizontal, 'Event frequency does not change zoom distance');

const holdTwo = controls();
holdTwo.mouseButtons.LEFT = MOUSE.DOLLY; // Same mapping used while holding 2 in the viewport.
close(drag(holdTwo, [[200, 200]], 0), horizontal, 'Hold-2 left drag uses horizontal zoom');

const wheelFast = controls();
const wheelOriginal = controls(OrbitControls);
wheelOriginal.zoomSpeed = 1.5; // Whitehound v0.8.1 sensitivity.
for (const control of [wheelFast, wheelOriginal]) {
  control._handleMouseWheel({clientX: 100, clientY: 200, deltaY: -100});
}
close(Math.log(distance(wheelFast) / 10) / Math.log(distance(wheelOriginal) / 10), 2, 'Wheel sensitivity is doubled from v0.8.1');
close(distance(wheelFast), horizontal, 'Wheel and drag share zoomSpeed');

const originalDrag = controls(OrbitControls);
originalDrag.zoomSpeed = 1.5;
originalDrag._onMouseDown({button: 2, clientX: 100, clientY: 200});
originalDrag._onMouseMove({clientX: 100, clientY: 100});
close(Math.log(horizontal / 10) / Math.log(distance(originalDrag) / 10), 2, 'Drag sensitivity is doubled from v0.8.1');

console.log(JSON.stringify({checks, zoomSpeed: fast.zoomSpeed, dragAxis: 'horizontal', webglRequired: false}));
