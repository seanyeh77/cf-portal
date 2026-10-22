// Applies the saved colour theme before the first paint.
(function () {
  try {
    var t = localStorage.getItem('cfp-theme');
    if (t === 'dark' || t === 'light') document.documentElement.setAttribute('data-theme', t);
  } catch (e) {}
})();
