import { OrbitControls } from './vendor/OrbitControls.js';

/** Cinema 4D-style horizontal drag zoom, twice the sensitivity of v0.8.1. */
export class PreviewOrbitControls extends OrbitControls {
  constructor(camera, element = null) {
    super(camera, element);
    this.zoomSpeed = 3;
  }

  _handleMouseDownDolly(event) {
    this._updateZoomParameters(event.clientX, event.clientY);
    this._dollyStart.set(event.clientX, event.clientY);
  }

  _handleMouseMoveDolly(event) {
    this._dollyEnd.set(event.clientX, event.clientY);
    this._dollyDelta.subVectors(this._dollyEnd, this._dollyStart);
    const horizontalPixels = this._dollyDelta.x;
    // Use pixel magnitude, not just its sign, so event frequency does not
    // change sensitivity. Drag right to approach, left to move away.
    if (horizontalPixels > 0) this._dollyIn(this._getZoomScale(horizontalPixels));
    else if (horizontalPixels < 0) this._dollyOut(this._getZoomScale(horizontalPixels));
    this._dollyStart.copy(this._dollyEnd);
    if (horizontalPixels !== 0) this.update();
  }
}
