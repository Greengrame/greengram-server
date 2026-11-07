import * as echarts from 'echarts/core';
import { BarChart, LineChart } from 'echarts/charts';
import { BrushComponent, GridComponent, MarkLineComponent, ToolboxComponent, TooltipComponent } from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';
import { Chart, TimeseriesChart } from '@cloudflare/kumo';
import { useTheme } from './theme.js';
import { number, shortDay } from './format.js';

echarts.use([BarChart, LineChart, GridComponent, TooltipComponent, BrushComponent, ToolboxComponent, MarkLineComponent, CanvasRenderer]);

// echarts needs real colours, not css variables: telegram's accent per mode, secondary text for a second series
export function ink(dark, secondary = false) {
    if (secondary) return dark ? '#aaaaaa' : '#707579';
    return dark ? '#8774e1' : '#3390ec';
}

function dayStamp(day) {
    return Date.parse(day + 'T00:00:00Z');
}

export function DailyChart({ series, type = 'bar', height = 220, label }) {
    let { dark } = useTheme();
    return <TimeseriesChart echarts={echarts} isDarkMode={dark} type={type} height={height} gradient={type === 'line'} ariaDescription={label}
        data={series.map((entry, index) => ({ name: entry.name, color: ink(dark, index > 0), data: entry.points.map(point => [dayStamp(point.day), point.value]) }))}
        xAxisTickFormat={value => shortDay(new Date(value).toISOString().slice(0, 10))}
        tooltipTimestampFormat={value => shortDay(new Date(value).toISOString().slice(0, 10))}
        tooltipValueFormat={value => number(value)}
        yAxisTickFormat={value => number(value)}
        yAxisMinInterval={1} />;
}

export function MinuteChart({ values, height = 200 }) {
    let { dark } = useTheme();
    let now = Math.floor(Date.now() / 60000) * 60000;
    return <TimeseriesChart echarts={echarts} isDarkMode={dark} type="bar" height={height}
        data={[{ name: 'Requests', color: ink(dark), data: values.map((value, index) => [now - (values.length - 1 - index) * 60000, value]) }]}
        xAxisTickFormat={value => new Date(value).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
        tooltipTimestampFormat={value => new Date(value).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
        tooltipValueFormat={value => number(value)}
        yAxisMinInterval={1} />;
}

export function HourChart({ values, height = 220 }) {
    let { dark } = useTheme();
    let options = {
        grid: { left: 40, right: 8, top: 12, bottom: 28 },
        tooltip: { trigger: 'axis', valueFormatter: value => number(value) + ' messages' },
        xAxis: { type: 'category', data: values.map((_, hour) => String(hour).padStart(2, '0') + ':00'), axisTick: { show: false }, axisLabel: { interval: 3 } },
        yAxis: { type: 'value', minInterval: 1 },
        series: [{ type: 'bar', name: 'Messages', data: values, itemStyle: { color: ink(dark), borderRadius: [3, 3, 0, 0] }, barCategoryGap: '25%' }]
    };
    return <Chart echarts={echarts} isDarkMode={dark} height={height} options={options} />;
}
