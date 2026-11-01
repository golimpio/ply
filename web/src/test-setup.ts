// jsdom does not implement scrolling; the router scrolls to the top on navigation.
window.scrollTo = () => {};
