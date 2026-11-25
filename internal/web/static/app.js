(function () {
  var times = document.querySelectorAll("time[datetime]");
  for (var i = 0; i < times.length; i++) {
    var el = times[i];
    var iso = el.getAttribute("datetime");
    var d = new Date(iso);
    if (!isNaN(d.getTime())) {
      el.setAttribute("title", iso);
      el.textContent = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
    }
  }
})();
