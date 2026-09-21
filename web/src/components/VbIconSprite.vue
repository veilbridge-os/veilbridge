<script setup lang="ts">
// The icon set of the accepted mockups (M2.7), as one sprite mounted once.
//
// Until now the panel drew no icons at all: the mockups carry 35 of them,
// every menu item has one, and the code had two — both belonging to Element
// Plus alerts. The whole set is 4 kB of path data, so it ships inline rather
// than as a request the device would have to serve.
//
// They are stroke-drawn and take their colour from the text around them
// (`stroke: currentColor`), which is what lets one sprite serve both themes.
</script>

<template>
  <svg width="0" height="0" style="position: absolute" aria-hidden="true" focusable="false">
    <defs>
    <g id="i-alert"><path d="M10 3.2l7.3 13.3H2.7z"></path><path d="M10 8v3.6"></path><circle cx="10" cy="14" r=".9" fill="currentColor" stroke="none"></circle></g>
    <g id="i-bridge"><path d="M2.5 15h15M4.6 15v-3.4a5.4 5.4 0 0 1 10.8 0V15M10 15V9.4M7.2 15v-4.4M12.8 15v-4.4"></path></g>
    <g id="i-cable"><path d="M6.2 2.8v4.2M13.8 2.8v4.2M3.8 7h12.4v2.4a6.2 6.2 0 0 1-12.4 0z"></path><path d="M10 15.6v1.8"></path></g>
    <g id="i-check"><path d="M4.5 10.4l3.6 3.6 7.4-8"></path></g>
    <g id="i-chev"><path d="M8 5l5 5-5 5"></path></g>
    <g id="i-clock"><circle cx="10" cy="10" r="7.5"></circle><path d="M10 5.6V10l3 1.8"></path></g>
    <g id="i-close"><path d="M5 5l10 10M15 5L5 15"></path></g>
    <g id="i-dash"><rect x="2.5" y="2.5" width="6" height="6" rx="1.5"></rect><rect x="11.5" y="2.5" width="6" height="6" rx="1.5"></rect><rect x="2.5" y="11.5" width="6" height="6" rx="1.5"></rect><rect x="11.5" y="11.5" width="6" height="6" rx="1.5"></rect></g>
    <g id="i-dev"><rect x="2.5" y="4" width="15" height="10" rx="1.5"></rect><path d="M7 17.5h6"></path></g>
    <g id="i-disk"><rect x="2.5" y="6.5" width="15" height="7" rx="1.5"></rect><path d="M5.5 10h2M14.5 10h-2"></path></g>
    <g id="i-draft"><path d="M4.5 3.5h7l4 4v9a1 1 0 0 1-1 1h-10a1 1 0 0 1-1-1v-12a1 1 0 0 1 1-1z"></path><path d="M11.5 3.5v4h4M7 11h6M7 14h4"></path></g>
    <g id="i-fork"><circle cx="5.5" cy="4.5" r="2"></circle><circle cx="14.5" cy="4.5" r="2"></circle><circle cx="10" cy="16" r="2"></circle><path d="M5.5 6.5v2a2 2 0 0 0 2 2h5a2 2 0 0 0 2-2v-2M10 10.5V14"></path></g>
    <g id="i-globe"><circle cx="10" cy="10" r="7.5"></circle><path d="M2.5 10h15M10 2.5c2 2.4 3 4.9 3 7.5s-1 5.1-3 7.5c-2-2.4-3-4.9-3-7.5s1-5.1 3-7.5z"></path></g>
    <g id="i-lock"><rect x="4" y="8.5" width="12" height="8.5" rx="1.6"></rect><path d="M6.8 8.5V6.4a3.2 3.2 0 0 1 6.4 0v2.1"></path></g>
    <g id="i-loop"><path d="M10 3.2a6.8 6.8 0 0 1 6.6 5.2"></path><path d="M10 16.8a6.8 6.8 0 0 1-6.6-5.2"></path><path d="M14.2 8.4h2.8V5.6M5.8 11.6H3v2.8"></path></g>
    <g id="i-mem"><rect x="4.5" y="4.5" width="11" height="11" rx="1.6"></rect><path d="M7.8 2.6v1.9M12.2 2.6v1.9M7.8 15.5v1.9M12.2 15.5v1.9M2.6 7.8h1.9M2.6 12.2h1.9M15.5 7.8h1.9M15.5 12.2h1.9"></path></g>
    <g id="i-menu"><path d="M3.5 5.5h13M3.5 10h13M3.5 14.5h13"></path></g>
    <g id="i-net"><rect x="2.5" y="12.5" width="5" height="5" rx="1"></rect><rect x="12.5" y="12.5" width="5" height="5" rx="1"></rect><rect x="7.5" y="2.5" width="5" height="5" rx="1"></rect><path d="M10 7.5v2.8M5 12.5v-2.2h10v2.2"></path></g>
    <g id="i-node"><rect x="2.8" y="3.2" width="14.4" height="5.4" rx="1.4"></rect><rect x="2.8" y="11.4" width="14.4" height="5.4" rx="1.4"></rect><path d="M5.6 5.9h.01M5.6 14.1h.01"></path></g>
    <g id="i-out"><path d="M12 5.5V4a1.5 1.5 0 0 0-1.5-1.5h-6A1.5 1.5 0 0 0 3 4v12a1.5 1.5 0 0 0 1.5 1.5h6A1.5 1.5 0 0 0 12 16v-1.5M8 10h9m0 0l-3-3m3 3l-3 3"></path></g>
    <g id="i-plug"><path d="M7 2.8v4.4M13 2.8v4.4M4.6 7.2h10.8v2.6a5.4 5.4 0 0 1-5.4 5.4 5.4 5.4 0 0 1-5.4-5.4z"></path><path d="M10 15.2v2.4"></path></g>
    <g id="i-plus"><path d="M10 4.5v11M4.5 10h11"></path></g>
    <g id="i-policy"><path d="M10 2.5l6.5 2.8v4.9c0 3.9-2.7 6.6-6.5 7.8-3.8-1.2-6.5-3.9-6.5-7.8V5.3z"></path></g>
    <g id="i-rule"><circle cx="5" cy="5" r="2.5"></circle><circle cx="15" cy="15" r="2.5"></circle><path d="M5 7.5v3.5a3.5 3.5 0 0 0 3.5 3.5h3.9"></path></g>
    <g id="i-search"><circle cx="8.8" cy="8.8" r="5.3"></circle><path d="M12.8 12.8l4 4"></path></g>
    <g id="i-snow"><path d="M10 3v14M4 6.5l12 7M16 6.5l-12 7"></path></g>
    <g id="i-sys"><path d="M4 6h12M4 10h12M4 14h12"></path><circle cx="8" cy="6" r="1.7"></circle><circle cx="13" cy="10" r="1.7"></circle><circle cx="7" cy="14" r="1.7"></circle></g>
    <g id="i-tabs"><rect x="2.5" y="5.5" width="11" height="11" rx="1.4"></rect><path d="M6.5 5.5v-2h11v11h-2"></path></g>
    <g id="i-target"><circle cx="10" cy="10" r="7"></circle><circle cx="10" cy="10" r="2.6"></circle><path d="M10 .8v2.6M10 16.6v2.6M.8 10h2.6M16.6 10h2.6"></path></g>
    <g id="i-trash"><path d="M4.5 5.5h11M8 5.5V4a1 1 0 0 1 1-1h2a1 1 0 0 1 1 1v1.5M6 5.5l.7 10a1 1 0 0 0 1 .9h4.6a1 1 0 0 0 1-.9l.7-10"></path></g>
    <g id="i-undo"><path d="M4 9.5A6.5 6.5 0 1 1 6.6 15"></path><path d="M3.2 5v4.5h4.5"></path></g>
    <g id="i-usb"><path d="M10 17.5V4.5"></path><path d="M7.2 7.3L10 4.5l2.8 2.8"></path><path d="M10 12.4l3.6-2.2v-2M10 14.2L6.4 12v-1.6"></path><circle cx="13.6" cy="7.6" r="1.2"></circle><rect x="5.2" y="9" width="2.4" height="2.4" rx=".4"></rect></g>
    <g id="i-v6"><path d="M2.8 6.5h14.4M2.8 13.5h14.4"></path><path d="M7.6 3.5L5.4 16.5M14.6 3.5l-2.2 13"></path></g>
    <g id="i-wifi"><path d="M2.6 8.2a11 11 0 0 1 14.8 0M5.6 11.4a6.6 6.6 0 0 1 8.8 0"></path><circle cx="10" cy="15.2" r="1.2"></circle></g>
    <g id="i-wifi-off"><path d="M2.6 8.2a11 11 0 0 1 6.2-3M17.4 8.2a11 11 0 0 0-3.1-2.1M5.6 11.4a6.6 6.6 0 0 1 2.2-1.4"></path><circle cx="10" cy="15.2" r="1.2"></circle><path d="M3 3l14 14"></path></g>
    </defs>
  </svg>
</template>
