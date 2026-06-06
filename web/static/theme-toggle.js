// Theme toggle: flip between light and dark, persist the choice.
// The initial theme is applied by an inline script in base.html before paint
// so there's no flash; this file only handles user-driven toggling.
(function () {
  var button = document.getElementById("theme-toggle");
  if (!button) return;

  button.addEventListener("click", function () {
    var current = document.documentElement.getAttribute("data-theme") === "dark" ? "dark" : "light";
    var next = current === "dark" ? "light" : "dark";
    document.documentElement.setAttribute("data-theme", next);
    try {
      localStorage.setItem("hearth-theme", next);
    } catch (e) { /* storage unavailable — choice won't persist, that's fine */ }
  });
})();
