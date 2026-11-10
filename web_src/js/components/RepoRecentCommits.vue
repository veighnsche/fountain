<script>
import {SvgIcon} from '../svg.js';
import {
  Chart,
  Tooltip,
  BarElement,
  LinearScale,
  TimeScale,
} from 'chart.js';
import {createGraphRequest} from '../native-graph.js';
import {Bar} from 'vue-chartjs';
import {
  startDaysBetween,
  firstStartDateAfterDate,
  fillEmptyStartDaysWithZeroes,
} from '../utils/time.js';
import {chartJsColors} from '../utils/color.js';
import 'chartjs-adapter-dayjs-4/dist/chartjs-adapter-dayjs-4.esm';

Chart.defaults.color = chartJsColors.text;
Chart.defaults.borderColor = chartJsColors.border;

Chart.register(
  TimeScale,
  LinearScale,
  BarElement,
  Tooltip,
);

export default {
  components: {Bar, SvgIcon},
  props: {
    repoLink: {type: String, default: () => window.config.pageData.repoLink || ''},
    locale: {
      type: Object,
      required: true,
    },
  },
  data: () => ({
    isLoading: false,
    graphRequest: createGraphRequest(),
    errorText: '',
    data: [],
  }),
  mounted() {
    this.fetchGraphData();
  },
  beforeUnmount() {
    this.graphRequest.dispose();
  },
  methods: {
    async fetchGraphData() {
      return this.graphRequest.run(`${this.repoLink}/activity/recent-commits/data`, {
        apply: (data) => {
          const start = Object.values(data)[0].week;
          const end = firstStartDateAfterDate(new Date());
          const startDays = startDaysBetween(start, end);
          this.data = fillEmptyStartDaysWithZeroes(startDays, data).slice(-52);
          this.errorText = '';
        },
        error: (message) => { this.errorText = message },
        loading: (value) => { this.isLoading = value },
      });
    },

    toGraphData(data) {
      return {
        datasets: [
          {
            data: data.map((i) => ({x: i.week, y: i.commits})),
            label: 'Commits',
            backgroundColor: chartJsColors['commits'],
            borderWidth: 0,
            tension: 0.3,
          },
        ],
      };
    },

    getOptions() {
      return {
        responsive: true,
        maintainAspectRatio: false,
        animation: true,
        scales: {
          x: {
            type: 'time',
            grid: {
              display: false,
            },
            time: {
              minUnit: 'week',
            },
            ticks: {
              maxRotation: 0,
              maxTicksLimit: 52,
            },
          },
          y: {
            ticks: {
              maxTicksLimit: 6,
            },
          },
        },
      };
    },
  },
};
</script>
<template>
  <div>
    <div class="ui header tw-flex tw-items-center tw-justify-between">
      {{ isLoading ? locale.loadingTitle : errorText ? locale.loadingTitleFailed : locale.recentCommitsTitle }}
    </div>
    <div class="tw-flex ui segment main-graph">
      <div v-if="isLoading || errorText !== ''" class="gt-tc tw-m-auto">
        <div v-if="isLoading">
          <SvgIcon name="octicon-sync" class="tw-mr-2 job-status-rotate"/>
          {{ locale.loadingInfo }}
        </div>
        <div v-else class="text red">
          <SvgIcon name="octicon-x-circle-fill"/>
          {{ errorText }}
        </div>
      </div>
      <Bar
        v-memo="data" v-if="data.length !== 0"
        :data="toGraphData(data)" :options="getOptions()"
      />
    </div>
  </div>
</template>
<style scoped>
.main-graph {
  height: 250px;
}
</style>
