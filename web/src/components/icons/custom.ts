// The nine icons Element Plus does not have (M2.9).
//
// Everything else in the panel now uses @element-plus/icons-vue, exactly as
// the accepted artboards do. These nine have no counterpart there — a globe,
// the Wi-Fi states, a cable, a plug, USB, the "freeze" mark, the IPv6 mark and
// the brand glyph — so they stay ours, drawn to the same optical weight the
// artboards use (a 1.25 stroke on a 20 grid is the 64/1024 of Element Plus's
// own grid) and coloured by the surrounding text.
//
// The path data is copied verbatim from the artboards in
// _internal/design/system/source, so the product and the mockups draw the same
// shape. It is static markup authored here, not input — hence innerHTML.
import { defineComponent, h } from 'vue'

function strokeIcon(name: string, body: string) {
  return defineComponent({
    name,
    render: () =>
      h('svg', {
        viewBox: '0 0 20 20',
        fill: 'none',
        stroke: 'currentColor',
        'stroke-width': 1.25,
        'stroke-linecap': 'round',
        'stroke-linejoin': 'round',
        'aria-hidden': 'true',
        focusable: 'false',
        innerHTML: body,
      }),
  })
}

export const VbBridge = strokeIcon(
  'VbBridge',
  '<path d="M2.5 15h15M4.6 15v-3.4a5.4 5.4 0 0 1 10.8 0V15M10 15V9.4M7.2 15v-4.4M12.8 15v-4.4"></path>',
)

export const VbCable = strokeIcon(
  'VbCable',
  '<path d="M6.2 2.8v4.2M13.8 2.8v4.2M3.8 7h12.4v2.4a6.2 6.2 0 0 1-12.4 0z"></path><path d="M10 15.6v1.8"></path>',
)

export const VbGlobe = strokeIcon(
  'VbGlobe',
  '<circle cx="10" cy="10" r="7.5"></circle><path d="M2.5 10h15M10 2.5c2 2.4 3 4.9 3 7.5s-1 5.1-3 7.5c-2-2.4-3-4.9-3-7.5s1-5.1 3-7.5z"></path>',
)

export const VbPlug = strokeIcon(
  'VbPlug',
  '<path d="M7 2.8v4.4M13 2.8v4.4M4.6 7.2h10.8v2.6a5.4 5.4 0 0 1-5.4 5.4 5.4 5.4 0 0 1-5.4-5.4z"></path><path d="M10 15.2v2.4"></path>',
)

export const VbSnow = strokeIcon('VbSnow', '<path d="M10 3v14M4 6.5l12 7M16 6.5l-12 7"></path>')

export const VbUsb = strokeIcon(
  'VbUsb',
  '<path d="M10 17.5V4.5"></path><path d="M7.2 7.3L10 4.5l2.8 2.8"></path><path d="M10 12.4l3.6-2.2v-2M10 14.2L6.4 12v-1.6"></path><circle cx="13.6" cy="7.6" r="1.2"></circle><rect x="5.2" y="9" width="2.4" height="2.4" rx=".4"></rect>',
)

export const VbIPv6 = strokeIcon(
  'VbIPv6',
  '<path d="M2.8 6.5h14.4M2.8 13.5h14.4"></path><path d="M7.6 3.5L5.4 16.5M14.6 3.5l-2.2 13"></path>',
)

export const VbWiFi = strokeIcon(
  'VbWiFi',
  '<path d="M2.6 8.2a11 11 0 0 1 14.8 0M5.6 11.4a6.6 6.6 0 0 1 8.8 0"></path><circle cx="10" cy="15.2" r="1.2"></circle>',
)

export const VbWiFiOff = strokeIcon(
  'VbWiFiOff',
  '<path d="M2.6 8.2a11 11 0 0 1 6.2-3M17.4 8.2a11 11 0 0 0-3.1-2.1M5.6 11.4a6.6 6.6 0 0 1 2.2-1.4"></path><circle cx="10" cy="15.2" r="1.2"></circle><path d="M3 3l14 14"></path>',
)
