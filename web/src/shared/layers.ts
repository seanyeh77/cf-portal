// Tracks open modal dialogs so floating elements (tooltips, menus, toasts)
// can be placed inside the top-most dialog and stay above the modal layer.

const stack: HTMLElement[] = [];
const listeners = new Set<() => void>();

export function pushLayer(el: HTMLElement) {
  stack.push(el);
  listeners.forEach((l) => l());
}

export function popLayer(el: HTMLElement) {
  const i = stack.lastIndexOf(el);
  if (i >= 0) stack.splice(i, 1);
  listeners.forEach((l) => l());
}

/** The element floating UI should be appended to. */
export function topLayer(): HTMLElement {
  for (let i = stack.length - 1; i >= 0; i--) {
    if (stack[i].isConnected) return stack[i];
  }
  return document.body;
}

export function onLayerChange(fn: () => void) {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

/**
 * Keeps a floating element inside the top-most layer: in the open modal
 * dialog (so its backdrop does not cover the element), back in <body> once
 * that dialog closes.
 */
export function keepInTopLayer(el: HTMLElement) {
  const place = () => {
    const layer = topLayer();
    if (el.parentElement !== layer) layer.appendChild(el);
  };
  place();
  onLayerChange(place);
}
