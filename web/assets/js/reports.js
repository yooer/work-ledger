/* 报告页图表（ApexCharts 本地版） */
(function () {
    'use strict';
    var palette = ['#0f766e', '#2563eb', '#b45309', '#be123c', '#15803d', '#7c3aed'];
    function render(selector, options) {
        var el = document.querySelector(selector);
        if (!el || typeof window.ApexCharts === 'undefined') return;
        var chart = new window.ApexCharts(el, options);
        chart.render();
    }
    window.WLCharts = {
        bars: function (selector, labels, series) {
            render(selector, {
                chart: { type: 'bar', height: 260, toolbar: { show: false }, fontFamily: 'inherit' },
                series: series,
                xaxis: { categories: labels },
                colors: palette,
                plotOptions: { bar: { columnWidth: '55%', borderRadius: 3 } },
                dataLabels: { enabled: false },
                legend: { position: 'top' },
                noData: { text: '暂无数据' },
                yaxis: { labels: { formatter: function (v) { return Math.round(v); } } }
            });
        },
        donut: function (selector, labels, values) {
            render(selector, {
                chart: { type: 'donut', height: 260, fontFamily: 'inherit' },
                series: values,
                labels: labels,
                colors: ['#15803d', '#0f766e', '#2563eb', '#b45309', '#be123c'],
                legend: { position: 'bottom' },
                noData: { text: '暂无数据' }
            });
        }
    };
})();
